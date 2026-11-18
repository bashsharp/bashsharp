package interp

// Sprint: #319; Story: #1087; Story-ID: 96cd7f0c400d

import (
	"io"
	"strconv"
)

// goSourceLocalNativeWriter answers writes to standard dependency writers at
// the Runner's corresponding sink. These identities are minted by the
// authenticated dependency worker; arbitrary *os.File values remain native.
//
// Besides avoiding one request per small output fragment, this preserves the
// worker drain's existing contract: a sink failure does not become a program
// os.File.Write error, because the worker's pipe write already succeeded.
func goSourceLocalNativeWriter(req bashPPEvalRequest, q bashPPBridgeRequest) ([]bashPPBridgeValue, bool) {
	if req.Bridge == nil || q.Op != "call" || q.Selector != "Write" || q.Spread ||
		q.Receiver == nil || q.Receiver.Kind != "handle" || q.Receiver.Handle == 0 ||
		q.Receiver.Session != req.Bridge.id || len(q.Args) != 1 {
		return nil, false
	}
	data, ok := goSourceLocalWriterBytes(q.Args[0])
	if !ok {
		return nil, false
	}
	var writer io.Writer
	switch req.Bridge.localWriter(*q.Receiver) {
	case "stdout":
		writer = req.Stdout
	case "stderr":
		writer = req.Stderr
	case "discard":
		writer = io.Discard
	default:
		return nil, false
	}
	if writer != nil {
		_, _ = writer.Write(data)
	}
	return []bashPPBridgeValue{
		{Kind: "int", Type: "int", Text: strconv.Itoa(len(data))},
		{Kind: "nil", Type: "error"},
	}, true
}

func (s *bashPPNativeSession) localWriter(value bashPPBridgeValue) string {
	if value.Kind != "handle" || value.Handle == 0 || value.Session != s.id {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localWriters[value.Handle]
}

func (s *bashPPNativeSession) revokeLocalWriter(value bashPPBridgeValue) {
	if value.Kind != "handle" || value.Handle == 0 || value.Session != s.id {
		return
	}
	s.mu.Lock()
	delete(s.localWriters, value.Handle)
	s.mu.Unlock()
}

func goSourceLocalWriterBytes(value bashPPBridgeValue) ([]byte, bool) {
	if value.Kind != "slice" || value.Length < 0 || value.Length > len(value.Elements) ||
		(value.Type != "[]byte" && value.Type != "[]uint8") {
		return nil, false
	}
	data := make([]byte, value.Length)
	for i, element := range value.Elements[:value.Length] {
		n, err := strconv.ParseUint(element.Text, 10, 8)
		if err != nil || element.Kind != "int" && element.Kind != "uint" {
			return nil, false
		}
		data[i] = byte(n)
	}
	return data, true
}
