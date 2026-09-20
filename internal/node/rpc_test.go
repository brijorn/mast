package node

import (
	"encoding/json"
	"testing"

	"github.com/brijorn/mast/internal/transport"
)

// Every response an RPC waits on has to be listed in deliverPeerRPCResponse.
//
// A type left out of it is not a visible error: the peer does the work and
// answers, the answer is dropped on the floor here, and the caller fails ten
// seconds later with a context deadline — which reads as a peer that is slow or
// offline rather than a coordinator that threw the reply away. That is how the
// peer-owned `adb reverse` failed after the peer side was already working, so
// the pairing is asserted rather than left to review.
func TestDeliverPeerRPCResponseAcceptsRequestResponseTypes(t *testing.T) {
	for _, messageType := range []string{
		transport.TypeReverseResponse,
		transport.TypeClipboardGetResponse,
		transport.TypeDevToolsForwardResponse,
	} {
		t.Run(messageType, func(t *testing.T) {
			node := &Node{}
			responses := make(chan peerRPCResponse, 1)
			node.pending = map[string]chan peerRPCResponse{"msg-1": responses}

			raw := transport.RawMessage{Type: messageType, ID: "msg-1"}
			encoded, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}

			if !node.deliverPeerRPCResponse(raw, encoded) {
				t.Fatalf("%s was not recognised as an RPC response, so the caller waits for a reply that already arrived", messageType)
			}

			select {
			case got := <-responses:
				if got.messageType != messageType {
					t.Fatalf("delivered %s, want %s", got.messageType, messageType)
				}
			default:
				t.Fatalf("%s was recognised but never handed to the waiting call", messageType)
			}
		})
	}
}
