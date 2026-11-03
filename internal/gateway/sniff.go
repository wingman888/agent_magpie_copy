package gateway

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// usageSniffer picks the usage block out of a provider reply that is being
// copied to the client untouched. Streams are read line by line as they
// pass; a plain JSON body is kept and parsed at the end.
type usageSniffer struct {
	proto provider.Protocol
	sse   bool
	buf   []byte
	over  bool // the JSON body outgrew the cap; give up on it
	u     Usage
}

func newSniffer(proto provider.Protocol, contentType string) *usageSniffer {
	return &usageSniffer{proto: proto, sse: strings.HasPrefix(contentType, "text/event-stream")}
}

func (s *usageSniffer) write(b []byte) {
	if !s.sse {
		if len(s.buf)+len(b) > 8<<20 {
			s.over = true
			return
		}
		s.buf = append(s.buf, b...)
		return
	}
	s.buf = append(s.buf, b...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(s.buf[:i])
		s.buf = s.buf[i+1:]
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			if rest = bytes.TrimSpace(rest); len(rest) > 0 && rest[0] == '{' {
				s.parse(rest)
			}
		}
	}
	if len(s.buf) > 1<<20 { // a runaway line is not one we can use
		s.buf = s.buf[:0]
	}
}

func (s *usageSniffer) parse(b []byte) {
	switch s.proto {
	case provider.Chat:
		var v struct {
			Usage *cUsage `json:"usage"`
		}
		if json.Unmarshal(b, &v) == nil && v.Usage != nil {
			s.u.add(v.Usage.usage())
		}
	case provider.Responses:
		var v struct {
			Usage    *rUsage `json:"usage"`
			Response struct {
				Usage *rUsage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(b, &v) == nil {
			if v.Response.Usage != nil {
				s.u.add(v.Response.Usage.usage())
			} else if v.Usage != nil {
				s.u.add(v.Usage.usage())
			}
		}
	default:
		var v struct {
			Usage   *aUsage `json:"usage"`
			Message struct {
				Usage *aUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(b, &v) == nil {
			if v.Message.Usage != nil {
				s.u.add(v.Message.Usage.usage())
			}
			if v.Usage != nil {
				s.u.add(v.Usage.usage())
			}
		}
	}
}

// usage is what the reply reported; call it once the body has ended.
func (s *usageSniffer) usage() Usage {
	if !s.sse && !s.over && len(s.buf) > 0 {
		s.parse(bytes.TrimSpace(s.buf))
		s.buf = nil
	}
	return s.u
}
