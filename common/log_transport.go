package common

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/logging"
)

const (
	// LogReqStartMsg is the short line emitted when a request goes out. A
	// start line that never receives a matching Request/Response entry marks
	// an in-flight (possibly hung) request.
	LogReqStartMsg = `[DEBUG] [%s] → #%d %s %s`

	LogReqRespMsg = `[DEBUG] [%s] API Request/Response (#%d, %s):
----[ REQUEST ]---------------------------------------
%s
----[ RESPONSE ]--------------------------------------
%s
------------------------------------------------------`

	// LogTransportErrMsg is emitted when the wrapped transport fails while
	// debug logging is disabled; it carries no request dump.
	LogTransportErrMsg = `[ERROR] [%s] API transport error (#%d, %s): %v`

	// LogTransportErrMsgMerged is the debug-logging counterpart: the failed
	// transaction in a single entry, including the request that failed.
	LogTransportErrMsgMerged = `[ERROR] [%s] API Request/Response (#%d, %s): %v
----[ REQUEST ]---------------------------------------
%s
------------------------------------------------------`
)

// requestID is a process-wide monotonically increasing counter used to
// correlate the start line and the merged Request/Response entry of each
// round trip. Concurrent round trips handled by different transport
// instances (the SDKv2 chain and the manifest chain each build their own
// LogTransport) therefore never share an ID, so a #N in the log
// unambiguously identifies one request/response pair.
var requestID atomic.Uint64

type LogTransport struct {
	name      string
	transport http.RoundTripper
}

func (t *LogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The ID is assigned unconditionally: it is cheap and it ties the start
	// line to the merged entry below. Whether anything is logged at all is
	// decided per request by the log level.
	id := requestID.Add(1)

	// The request dump is buffered until the round trip finishes so that
	// request and response are printed as a single log entry instead of
	// being interleaved with concurrent traffic.
	var reqSection string
	if logging.IsDebugOrHigher() {
		log.Printf(LogReqStartMsg, t.name, id, req.Method, req.URL.RequestURI())

		if reqData, err := httputil.DumpRequest(req, true); err != nil {
			reqSection = fmt.Sprintf("(request dump failed: %s)", err)
		} else {
			reqSection = prettyPrintJsonLines(reqData)
		}
	}

	start := time.Now()
	resp, err := t.transport.RoundTrip(req)
	elapsed := time.Since(start).Round(time.Microsecond)
	if err != nil {
		// Errors are always visible; with debug logging the failed request
		// is included in the same entry so the failure has its context.
		if reqSection != "" {
			log.Printf(LogTransportErrMsgMerged, t.name, id, elapsed, err, reqSection)
		} else {
			log.Printf(LogTransportErrMsg, t.name, id, elapsed, err)
		}
		return resp, err
	}

	if logging.IsDebugOrHigher() {
		var respSection string
		if respData, err := httputil.DumpResponse(resp, true); err != nil {
			respSection = fmt.Sprintf("(response dump failed: %s)", err)
		} else {
			respSection = prettyPrintJsonLines(respData)
		}
		log.Printf(LogReqRespMsg, t.name, id, elapsed, reqSection, respSection)
	}

	return resp, nil
}

func NewLogTransport(name string, t http.RoundTripper) *LogTransport {
	return &LogTransport{name, t}
}

// prettyPrintJsonLines iterates through a []byte line-by-line,
// transforming any lines that are complete json into pretty-printed json.
func prettyPrintJsonLines(b []byte) string {
	parts := strings.Split(string(b), "\n")
	for i, p := range parts {
		if b := []byte(p); json.Valid(b) {
			var out bytes.Buffer
			_ = json.Indent(&out, b, "", " ") // already checked for validity
			parts[i] = out.String()
		}
	}

	for i := range parts {
		parts[i] = removeSensitive(parts[i])
	}

	return strings.Join(parts, "\n")
}

var sensitiveKeywords = []string{"Authorization", "X-Security-Token", "stringData", "data", "secretName"}

func removeSensitive(s string) string {
	reg1 := regexp.MustCompile(fmt.Sprintf(`("%s"):\s*"(.*?)"`, strings.Join(sensitiveKeywords, `"|"`)))
	s = reg1.ReplaceAllString(s, `$1: "******"`)

	reg2 := regexp.MustCompile(fmt.Sprintf(`(%s):\s*(.*?)$`, strings.Join(sensitiveKeywords, `|`)))
	s = reg2.ReplaceAllString(s, `$1: ******`)

	return s
}
