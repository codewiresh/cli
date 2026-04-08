package relay

import (
	"context"

	"github.com/codewiresh/codewire/internal/relayapi"
)

type NodeEnrollmentResult = relayapi.NodeEnrollmentResult
type NodeRedeemResult = relayapi.NodeRedeemResult
type JoinResult = relayapi.JoinResult

func CreateNodeEnrollment(ctx context.Context, relayURL, networkID, nodeName, authToken string, uses int, ttl string) (*NodeEnrollmentResult, error) {
	return relayapi.CreateNodeEnrollment(ctx, relayURL, networkID, nodeName, authToken, uses, ttl)
}

func RedeemNodeEnrollment(ctx context.Context, relayURL, enrollmentToken, nodeName string) (*NodeRedeemResult, error) {
	return relayapi.RedeemNodeEnrollment(ctx, relayURL, enrollmentToken, nodeName)
}

func RegisterWithAuthToken(ctx context.Context, relayURL, networkID, nodeName, authToken string) (string, error) {
	return relayapi.RegisterWithAuthToken(ctx, relayURL, networkID, nodeName, authToken)
}

func JoinNetworkWithInvite(ctx context.Context, relayURL, authToken, inviteToken string) (*JoinResult, error) {
	return relayapi.JoinNetworkWithInvite(ctx, relayURL, authToken, inviteToken)
}

func RegisterWithInvite(ctx context.Context, relayURL, nodeName, inviteToken string) (*NodeRedeemResult, error) {
	return relayapi.RegisterWithInvite(ctx, relayURL, nodeName, inviteToken)
}

func SSHURI(relayURL, networkID, nodeName, nodeToken string, port int) string {
	return relayapi.SSHURI(relayURL, networkID, nodeName, nodeToken, port)
}
