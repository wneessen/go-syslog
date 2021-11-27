package parsesyslog

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"time"
)

// RFC5424Msg represents a log message in that matches RFC5424
type RFC5424Msg struct {
	buf bytes.Buffer
}

// ParseReader is the parser function that is able to interpret RFC5424 and
// satisfies the Parser interface
func (m *RFC5424Msg) parseReader(r io.Reader) (LogMsg, error) {
	l := LogMsg{
		Type: RFC5424,
	}

	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	ml, err := readMsgLength(br)
	if err != nil {
		return l, err
	}

	lr := io.LimitReader(br, int64(ml))
	br = bufio.NewReaderSize(lr, ml)
	if err := m.parseHeader(br, &l); err != nil {
		switch {
		case errors.Is(err, io.EOF):
			return l, ErrPrematureEOF
		default:
			return l, err
		}
	}
	if err := m.parseStructuredData(br, &l); err != nil {
		switch {
		case errors.Is(err, io.EOF):
			return l, ErrPrematureEOF
		default:
			return l, err
		}
	}

	if err := m.parseBOM(br, &l); err != nil {
		return l, nil
	}

	//rb := make([]byte, ml - l.Message.Len())
	md, err := io.ReadAll(br)
	if err != nil {
		return l, err
	}
	l.Message.Write(md)
	l.MsgLength = l.Message.Len()

	return l, nil
}

// parseHeader will try to parse the header of a RFC5424 syslog message and store
// it in the provided LogMsg pointer
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.2
func (m *RFC5424Msg) parseHeader(r *bufio.Reader, lm *LogMsg) error {
	if err := m.parsePriority(r, lm); err != nil {
		return err
	}
	if err := m.parseProtoVersion(r, lm); err != nil {
		return err
	}
	if err := m.parseTimestamp(r, lm); err != nil {
		return err
	}
	if err := m.parseHostname(r, lm); err != nil {
		return err
	}
	if err := m.parseAppName(r, lm); err != nil {
		return err
	}
	if err := m.parseProcID(r, lm); err != nil {
		return err
	}
	if err := m.parseMsgID(r, lm); err != nil {
		return err
	}

	return nil
}

// parseStructuredData will try to parse the SD of a RFC5424 syslog message and
// store it in the provided LogMsg pointer
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.2
// We are using a simple finite state machine here to parse through the different
// states of the parameters and elements
func (m *RFC5424Msg) parseStructuredData(r *bufio.Reader, lm *LogMsg) error {
	m.buf.Reset()

	nb, err := r.ReadByte()
	if err != nil {
		return err
	}
	if nb == '-' {
		_, err = r.ReadByte()
		if err != nil {
			return err
		}
		return nil
	}
	if nb != '[' {
		return ErrWrongSDFormat
	}

	var sds []StructuredDataElement
	var sd StructuredDataElement
	var sdp StructuredDataParam
	insideelem := true
	insideparam := false
	readname := false
	for {
		b, err := r.ReadByte()
		if err != nil {
			return err
		}
		if b == ']' {
			insideelem = false
			sds = append(sds, sd)
			sd = StructuredDataElement{}
			m.buf.Reset()
			continue
		}
		if b == '[' {
			insideelem = true
			readname = false
			continue
		}
		if b == ' ' && !readname {
			readname = true
			sd.ID = m.buf.String()
			m.buf.Reset()
		}
		if b == '=' && !insideparam {
			sdp.Name = m.buf.String()
			m.buf.Reset()
			continue
		}
		if b == '"' && !insideparam {
			insideparam = true
			continue
		}
		if b == '"' && insideparam {
			insideparam = false
			sdp.Value = m.buf.String()
			m.buf.Reset()
			sd.Param = append(sd.Param, sdp)
			sdp = StructuredDataParam{}
			continue
		}
		if b == ' ' && !insideelem {
			break
		}
		if b == ' ' && !insideparam {
			continue
		}
		m.buf.WriteByte(b)
	}
	lm.StructuredData = sds

	return nil
}

// parseBOM will try to parse the BOM (if any) of the RFC54524 header
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.4
func (m *RFC5424Msg) parseBOM(r *bufio.Reader, lm *LogMsg) error {
	bom, err := r.Peek(3)
	if err != nil {
		return err
	}
	if bytes.Equal(bom, []byte{0xEF, 0xBB, 0xBF}) {
		lm.HasBOM = true
	}
	return nil
}

// parsePriority will try to parse the priority part of the RFC54524 header
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.2.1
func (m *RFC5424Msg) parsePriority(r *bufio.Reader, lm *LogMsg) error {
	var b [1]byte
	var ps []byte
	_, err := r.Read(b[:])
	if err != nil {
		return err
	}
	if b[0] != '<' {
		return ErrWrongFormat
	}
	for {
		_, err := r.Read(b[:])
		if err != nil {
			return err
		}
		if b[0] == '>' {
			break
		}
		ps = append(ps, b[0])
	}
	p, err := atoi(ps)
	if err != nil {
		return ErrInvalidPrio
	}
	lm.Priority = Priority(p)
	lm.Facility = FacilityFromPrio(lm.Priority)
	lm.Severity = SeverityFromPrio(lm.Priority)
	return nil
}

// parseProtoVersion will try to parse the proto version part of the RFC54524 header
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.2.2
func (m *RFC5424Msg) parseProtoVersion(r *bufio.Reader, lm *LogMsg) error {
	b, _, err := readBytesUntilSpace(r)
	if err != nil {
		return err
	}
	pv, err := atoi(b)
	if err != nil {
		return ErrInvalidProtoVersion
	}
	lm.ProtoVersion = ProtoVersion(pv)
	return nil
}

// parseTimestamp will try to parse the timestamp (or NILVALUE) part of the
// RFC54524 header
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.2.3
func (m *RFC5424Msg) parseTimestamp(r *bufio.Reader, lm *LogMsg) error {
	_, err := readBytesUntilSpaceOrNilValue(r, &m.buf)
	if err != nil {
		return err
	}
	if m.buf.Len() == 0 {
		return nil
	}
	if m.buf.Bytes()[0] == '-' {
		return nil
	}
	ts, err := time.Parse(time.RFC3339, m.buf.String())
	if err != nil {
		return ErrInvalidTimestamp
	}
	lm.Timestamp = ts
	return nil
}

// parseHostname will try to read the hostname part of the RFC54524 header
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.2.4
func (m *RFC5424Msg) parseHostname(r *bufio.Reader, lm *LogMsg) error {
	_, err := readBytesUntilSpaceOrNilValue(r, &m.buf)
	if err != nil {
		return err
	}
	if m.buf.Len() == 0 {
		return nil
	}
	if m.buf.Bytes()[0] == '-' {
		return nil
	}
	lm.Hostname = m.buf.String()
	return nil
}

// parseAppName will try to read the app name part of the RFC54524 header
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.2.5
func (m *RFC5424Msg) parseAppName(r *bufio.Reader, lm *LogMsg) error {
	_, err := readBytesUntilSpaceOrNilValue(r, &m.buf)
	if err != nil {
		return err
	}
	if m.buf.Len() == 0 {
		return nil
	}
	if m.buf.Bytes()[0] == '-' {
		return nil
	}
	lm.AppName = m.buf.String()
	return nil
}

// parseProcID will try to read the process ID part of the RFC54524 header
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.2.6
func (m *RFC5424Msg) parseProcID(r *bufio.Reader, lm *LogMsg) error {
	_, err := readBytesUntilSpaceOrNilValue(r, &m.buf)
	if err != nil {
		return err
	}
	if m.buf.Len() == 0 {
		return nil
	}
	if m.buf.Bytes()[0] == '-' {
		return nil
	}
	lm.ProcID = m.buf.String()
	return nil
}

// parseMsgID will try to read the message ID part of the RFC54524 header
// See: https://datatracker.ietf.org/doc/html/rfc5424#section-6.2.7
func (m *RFC5424Msg) parseMsgID(r *bufio.Reader, lm *LogMsg) error {
	_, err := readBytesUntilSpaceOrNilValue(r, &m.buf)
	if err != nil {
		return err
	}
	if m.buf.Len() == 0 {
		return nil
	}
	if m.buf.Bytes()[0] == '-' {
		return nil
	}
	lm.MsgID = m.buf.String()
	return nil
}
