package terminal

import "bytes"

type outputParserState int

const (
	outputStateGround outputParserState = iota
	outputStateEsc
	outputStateCSI
	outputStateOSC
	outputStateOSCEsc
)

// OutputState tracks enough VT state to know when it is safe for the client to
// inject its own status-bar escape sequences.
type OutputState struct {
	state     outputParserState
	csiParams []byte
	altScreen bool
}

func NewOutputState() *OutputState {
	return &OutputState{}
}

func (s *OutputState) AltScreen() bool {
	return s.altScreen
}

func (s *OutputState) SafeToInject() bool {
	return s.state == outputStateGround
}

func (s *OutputState) Feed(buf []byte) {
	for _, b := range buf {
		switch s.state {
		case outputStateGround:
			if b == 0x1b {
				s.state = outputStateEsc
			}
		case outputStateEsc:
			switch b {
			case '[':
				s.state = outputStateCSI
				s.csiParams = s.csiParams[:0]
			case ']':
				s.state = outputStateOSC
			default:
				s.state = outputStateGround
			}
		case outputStateCSI:
			switch {
			case b >= 0x30 && b <= 0x3f:
				s.csiParams = append(s.csiParams, b)
			case b >= 0x20 && b <= 0x2f:
				// Intermediate byte.
			case b >= 0x40 && b <= 0x7e:
				s.applyCSI(b)
				s.state = outputStateGround
				s.csiParams = s.csiParams[:0]
			default:
				s.state = outputStateGround
				s.csiParams = s.csiParams[:0]
			}
		case outputStateOSC:
			if b == 0x07 {
				s.state = outputStateGround
			} else if b == 0x1b {
				s.state = outputStateOSCEsc
			}
		case outputStateOSCEsc:
			if b == '\\' {
				s.state = outputStateGround
			} else {
				s.state = outputStateOSC
			}
		}
	}
}

func (s *OutputState) applyCSI(final byte) {
	if final != 'h' && final != 'l' {
		return
	}
	enable := final == 'h'
	switch {
	case bytes.Equal(s.csiParams, []byte("?1049")):
		s.altScreen = enable
	case bytes.Equal(s.csiParams, []byte("?1047")):
		s.altScreen = enable
	case bytes.Equal(s.csiParams, []byte("?47")):
		s.altScreen = enable
	}
}
