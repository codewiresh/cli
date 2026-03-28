package relay

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/codewiresh/codewire/internal/store"
)

var (
	errEnrollmentTokenRequired    = errors.New("enrollment_token required")
	errEnrollmentInvalid          = errors.New("invalid or expired enrollment token")
	errEnrollmentNodeNameMismatch = errors.New("node_name does not match enrollment")
	errEnrollmentNodeNameRequired = errors.New("node_name required")
)

type redeemEnrollmentOptions struct {
	peerURL  string
	githubID *int64
}

func createNodeEnrollment(ctx context.Context, st store.Store, networkID, ownerSubject, issuedBy, nodeName string, uses int, ttl time.Duration) (*store.NodeEnrollment, string, error) {
	if uses <= 0 {
		uses = 1
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	now := time.Now().UTC()
	token := "cw_enr_" + generateToken()
	enrollment := &store.NodeEnrollment{
		ID:            generateToken(),
		NetworkID:     strings.TrimSpace(networkID),
		OwnerSubject:  strings.TrimSpace(ownerSubject),
		IssuedBy:      strings.TrimSpace(issuedBy),
		NodeName:      strings.TrimSpace(nodeName),
		TokenHash:     hashEnrollmentToken(token),
		UsesRemaining: uses,
		ExpiresAt:     now.Add(ttl),
		CreatedAt:     now,
	}
	if err := st.NodeEnrollmentCreate(ctx, *enrollment); err != nil {
		return nil, "", err
	}
	return enrollment, token, nil
}

func redeemNodeEnrollment(ctx context.Context, st store.Store, enrollmentToken, nodeName string, opts redeemEnrollmentOptions) (*NodeRedeemResult, error) {
	enrollmentToken = strings.TrimSpace(enrollmentToken)
	if enrollmentToken == "" {
		return nil, errEnrollmentTokenRequired
	}

	now := time.Now().UTC()
	tokenHash := hashEnrollmentToken(enrollmentToken)
	enrollment, err := st.NodeEnrollmentGetByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, err
	}
	if enrollment == nil || enrollment.ExpiresAt.Before(now) || enrollment.UsesRemaining <= 0 {
		return nil, errEnrollmentInvalid
	}

	nodeName = strings.TrimSpace(nodeName)
	switch {
	case enrollment.NodeName != "" && nodeName == "":
		nodeName = enrollment.NodeName
	case enrollment.NodeName != "" && nodeName != enrollment.NodeName:
		return nil, errEnrollmentNodeNameMismatch
	case nodeName == "":
		return nil, errEnrollmentNodeNameRequired
	}

	consumed, err := st.NodeEnrollmentConsume(ctx, tokenHash, now)
	if err != nil {
		return nil, err
	}
	if consumed == nil || consumed.ExpiresAt.Before(now) {
		return nil, errEnrollmentInvalid
	}

	nodeToken := generateToken()
	node := store.NodeRecord{
		NetworkID:    consumed.NetworkID,
		Name:         nodeName,
		Token:        nodeToken,
		PeerURL:      strings.TrimSpace(opts.peerURL),
		GitHubID:     opts.githubID,
		OwnerSubject: consumed.OwnerSubject,
		AuthorizedBy: consumed.IssuedBy,
		EnrollmentID: consumed.ID,
		AuthorizedAt: now,
		LastSeenAt:   now,
	}
	if err := st.NodeRegister(ctx, node); err != nil {
		return nil, err
	}

	return &NodeRedeemResult{
		NodeToken:    nodeToken,
		NodeName:     nodeName,
		NetworkID:    consumed.NetworkID,
		EnrollmentID: consumed.ID,
	}, nil
}
