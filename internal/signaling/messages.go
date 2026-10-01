package signaling

import "encoding/json"

// Envelope is the JSON wrapper for every WebSocket message.
type Envelope struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// envelope builds a marshaled envelope from a typed payload.
func envelope(typ, requestID string, payload any) Envelope {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err == nil {
			raw = b
		}
	}
	return Envelope{Type: typ, RequestID: requestID, Payload: raw}
}

// Outgoing payloads.

type userRef struct {
	Extension string `json:"extension"`
	Name      string `json:"name"`
}

type ringAckPayload struct {
	CallID string  `json:"call_id"`
	Status string  `json:"status"`
	To     userRef `json:"to"`
}

type incomingCallPayload struct {
	CallID string  `json:"call_id"`
	From   userRef `json:"from"`
}

type callIDPayload struct {
	CallID string `json:"call_id"`
}

type busyPayload struct {
	Extension string `json:"extension"`
}

type unavailablePayload struct {
	Extension string `json:"extension"`
	Reason    string `json:"reason"`
}

type callEndedPayload struct {
	CallID string `json:"call_id"`
	Reason string `json:"reason"`
}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Incoming payload shapes.

type callRequest struct {
	To string `json:"to"`
}

type callIDRequest struct {
	CallID string `json:"call_id"`
}

type sdpRequest struct {
	CallID string `json:"call_id"`
	SDP    string `json:"sdp"`
}

type iceRequest struct {
	CallID    string          `json:"call_id"`
	Candidate json.RawMessage `json:"candidate"`
}
