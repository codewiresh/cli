package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time interface assertion.
var _ Store = (*PostgresStore)(nil)

// PostgresStore implements Store using a PostgreSQL connection pool.
type PostgresStore struct {
	pool    *pgxpool.Pool
	closeCh chan struct{}
}

// NewPostgresStore runs migrations, connects to PostgreSQL, and starts the
// background cleanup loop.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	if err := RunMigrations(databaseURL); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	s := &PostgresStore{pool: pool, closeCh: make(chan struct{})}
	go s.cleanupLoop()
	return s, nil
}

// Close shuts down the cleanup goroutine and closes the connection pool.
func (s *PostgresStore) Close() error {
	close(s.closeCh)
	s.pool.Close()
	return nil
}

// cleanupLoop periodically removes expired rows from time-bounded tables.
func (s *PostgresStore) cleanupLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			now := time.Now().UTC()
			ctx := context.Background()
			s.pool.Exec(ctx, "DELETE FROM kv WHERE expires_at IS NOT NULL AND expires_at < $1", now)
			s.pool.Exec(ctx, "DELETE FROM device_codes WHERE expires_at < $1", now)
			s.pool.Exec(ctx, "DELETE FROM sessions WHERE expires_at < $1", now)
			s.pool.Exec(ctx, "DELETE FROM oauth_state WHERE expires_at < $1", now)
			s.pool.Exec(ctx, "DELETE FROM invites WHERE expires_at < $1", now)
			s.pool.Exec(ctx, "DELETE FROM access_grants WHERE expires_at < $1", now)
			s.pool.Exec(ctx, "DELETE FROM oidc_sessions WHERE expires_at < $1", now)
			s.pool.Exec(ctx, "DELETE FROM oidc_device_flows WHERE expires_at < $1", now)
		}
	}
}

// ---------------------------------------------------------------------------
// KV Store
// ---------------------------------------------------------------------------

func (s *PostgresStore) KVSet(ctx context.Context, networkID, namespace, key string, value []byte, ttl *time.Duration) error {
	var expiresAt *time.Time
	if ttl != nil {
		t := time.Now().UTC().Add(*ttl)
		expiresAt = &t
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO kv (network_id, namespace, key, value, expires_at) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (network_id, namespace, key) DO UPDATE SET value = EXCLUDED.value, expires_at = EXCLUDED.expires_at`,
		networkID, namespace, key, value, expiresAt,
	)
	return err
}

func (s *PostgresStore) KVGet(ctx context.Context, networkID, namespace, key string) ([]byte, error) {
	var value []byte
	err := s.pool.QueryRow(ctx,
		"SELECT value FROM kv WHERE network_id = $1 AND namespace = $2 AND key = $3 AND (expires_at IS NULL OR expires_at > $4)",
		networkID, namespace, key, time.Now().UTC(),
	).Scan(&value)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return value, err
}

func (s *PostgresStore) KVDelete(ctx context.Context, networkID, namespace, key string) error {
	_, err := s.pool.Exec(ctx,
		"DELETE FROM kv WHERE network_id = $1 AND namespace = $2 AND key = $3",
		networkID, namespace, key,
	)
	return err
}

func (s *PostgresStore) KVList(ctx context.Context, networkID, namespace, prefix string) ([]KVEntry, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT key, value, expires_at FROM kv WHERE network_id = $1 AND namespace = $2 AND key LIKE $3 AND (expires_at IS NULL OR expires_at > $4)",
		networkID, namespace, prefix+"%", time.Now().UTC(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []KVEntry
	for rows.Next() {
		var e KVEntry
		if err := rows.Scan(&e.Key, &e.Value, &e.ExpiresAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ---------------------------------------------------------------------------
// Networks
// ---------------------------------------------------------------------------

func (s *PostgresStore) NetworkEnsure(ctx context.Context, networkID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO networks (network_id, created_at) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		networkID, time.Now().UTC(),
	)
	return err
}

func (s *PostgresStore) NetworkList(ctx context.Context) ([]Network, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			n.network_id,
			n.created_at,
			COALESCE(node_counts.count, 0),
			COALESCE(invite_counts.count, 0)
		FROM networks n
		LEFT JOIN (
			SELECT network_id, COUNT(*) AS count
			FROM nodes
			GROUP BY network_id
		) node_counts ON node_counts.network_id = n.network_id
		LEFT JOIN (
			SELECT network_id, COUNT(*) AS count
			FROM invites
			WHERE expires_at > $1
			GROUP BY network_id
		) invite_counts ON invite_counts.network_id = n.network_id
		ORDER BY n.network_id
	`, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var networks []Network
	for rows.Next() {
		var network Network
		if err := rows.Scan(&network.ID, &network.CreatedAt, &network.NodeCount, &network.InviteCount); err != nil {
			return nil, err
		}
		networks = append(networks, network)
	}
	return networks, rows.Err()
}

func (s *PostgresStore) NetworkListByMember(ctx context.Context, subject string) ([]Network, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			n.network_id,
			n.created_at,
			COALESCE(node_counts.count, 0),
			COALESCE(invite_counts.count, 0)
		FROM networks n
		INNER JOIN network_members nm ON nm.network_id = n.network_id
		LEFT JOIN (
			SELECT network_id, COUNT(*) AS count
			FROM nodes
			GROUP BY network_id
		) node_counts ON node_counts.network_id = n.network_id
		LEFT JOIN (
			SELECT network_id, COUNT(*) AS count
			FROM invites
			WHERE expires_at > $1
			GROUP BY network_id
		) invite_counts ON invite_counts.network_id = n.network_id
		WHERE nm.subject = $2
		ORDER BY n.network_id
	`, time.Now().UTC(), subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var networks []Network
	for rows.Next() {
		var network Network
		if err := rows.Scan(&network.ID, &network.CreatedAt, &network.NodeCount, &network.InviteCount); err != nil {
			return nil, err
		}
		networks = append(networks, network)
	}
	return networks, rows.Err()
}

func (s *PostgresStore) NetworkMemberGet(ctx context.Context, networkID, subject string) (*NetworkMember, error) {
	var member NetworkMember
	err := s.pool.QueryRow(ctx,
		`SELECT network_id, subject, role, created_at, created_by
		 FROM network_members
		 WHERE network_id = $1 AND subject = $2`,
		networkID, subject,
	).Scan(&member.NetworkID, &member.Subject, &member.Role, &member.CreatedAt, &member.CreatedBy)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &member, nil
}

func (s *PostgresStore) NetworkMemberUpsert(ctx context.Context, member NetworkMember) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO networks (network_id, created_at) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		member.NetworkID, time.Now().UTC(),
	); err != nil {
		return err
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO network_members (network_id, subject, role, created_at, created_by)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (network_id, subject) DO UPDATE SET
		   role = EXCLUDED.role`,
		member.NetworkID, member.Subject, member.Role, member.CreatedAt, member.CreatedBy,
	)
	return err
}

func (s *PostgresStore) NetworkMemberCount(ctx context.Context, networkID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM network_members WHERE network_id = $1`,
		networkID,
	).Scan(&count)
	return count, err
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

func (s *PostgresStore) GroupCreate(ctx context.Context, group Group) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`INSERT INTO networks (network_id, created_at) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		group.NetworkID, time.Now().UTC(),
	); err != nil {
		return err
	}

	res, err := tx.Exec(ctx,
		`INSERT INTO groups (network_id, name, created_at, created_by)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		group.NetworkID, group.Name, group.CreatedAt, group.CreatedBy,
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("group already exists")
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO group_policies (network_id, group_name, messages_policy, debug_policy, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT DO NOTHING`,
		group.NetworkID, group.Name, GroupMessagesInternalOnly, GroupDebugObserveOnly, group.CreatedAt,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *PostgresStore) GroupList(ctx context.Context, networkID string) ([]Group, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT network_id, name, created_at, created_by
		 FROM groups
		 WHERE network_id = $1
		 ORDER BY name`,
		networkID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []Group
	for rows.Next() {
		var group Group
		if err := rows.Scan(&group.NetworkID, &group.Name, &group.CreatedAt, &group.CreatedBy); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *PostgresStore) GroupGet(ctx context.Context, networkID, groupName string) (*Group, error) {
	var group Group
	err := s.pool.QueryRow(ctx,
		`SELECT network_id, name, created_at, created_by
		 FROM groups
		 WHERE network_id = $1 AND name = $2`,
		networkID, groupName,
	).Scan(&group.NetworkID, &group.Name, &group.CreatedAt, &group.CreatedBy)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &group, nil
}

func (s *PostgresStore) GroupDelete(ctx context.Context, networkID, groupName string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`DELETE FROM group_members
		 WHERE network_id = $1 AND group_name = $2`,
		networkID, groupName,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM group_policies
		 WHERE network_id = $1 AND group_name = $2`,
		networkID, groupName,
	); err != nil {
		return err
	}
	res, err := tx.Exec(ctx,
		`DELETE FROM groups
		 WHERE network_id = $1 AND name = $2`,
		networkID, groupName,
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("group not found")
	}

	return tx.Commit(ctx)
}

func (s *PostgresStore) GroupMemberAdd(ctx context.Context, member GroupMember) error {
	var exists int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM groups WHERE network_id = $1 AND name = $2`,
		member.NetworkID, member.GroupName,
	).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("group not found")
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO group_members (network_id, group_name, node_name, session_name, created_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT DO NOTHING`,
		member.NetworkID, member.GroupName, member.NodeName, member.SessionName, member.CreatedAt,
	)
	return err
}

func (s *PostgresStore) GroupMemberRemove(ctx context.Context, networkID, groupName, nodeName, sessionName string) error {
	res, err := s.pool.Exec(ctx,
		`DELETE FROM group_members
		 WHERE network_id = $1 AND group_name = $2 AND node_name = $3 AND session_name = $4`,
		networkID, groupName, nodeName, sessionName,
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("group member not found")
	}
	return nil
}

func (s *PostgresStore) GroupMemberList(ctx context.Context, networkID, groupName string) ([]GroupMember, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT network_id, group_name, node_name, session_name, created_at
		 FROM group_members
		 WHERE network_id = $1 AND group_name = $2
		 ORDER BY node_name, session_name`,
		networkID, groupName,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []GroupMember
	for rows.Next() {
		var member GroupMember
		if err := rows.Scan(&member.NetworkID, &member.GroupName, &member.NodeName, &member.SessionName, &member.CreatedAt); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *PostgresStore) GroupBindingsForSession(ctx context.Context, networkID, nodeName, sessionName string) ([]GroupBinding, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT gm.group_name,
		        COALESCE(gp.messages_policy, $1),
		        COALESCE(gp.debug_policy, $2)
		 FROM group_members gm
		 LEFT JOIN group_policies gp
		   ON gp.network_id = gm.network_id
		  AND gp.group_name = gm.group_name
		 WHERE gm.network_id = $3
		   AND gm.node_name = $4
		   AND gm.session_name = $5
		 ORDER BY gm.group_name`,
		GroupMessagesInternalOnly,
		GroupDebugObserveOnly,
		networkID,
		nodeName,
		sessionName,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bindings []GroupBinding
	for rows.Next() {
		var binding GroupBinding
		if err := rows.Scan(&binding.GroupName, &binding.MessagesPolicy, &binding.DebugPolicy); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func (s *PostgresStore) GroupPolicySet(ctx context.Context, policy GroupPolicy) error {
	var exists int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM groups WHERE network_id = $1 AND name = $2`,
		policy.NetworkID, policy.GroupName,
	).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("group not found")
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO group_policies (network_id, group_name, messages_policy, debug_policy, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (network_id, group_name) DO UPDATE SET
		   messages_policy = EXCLUDED.messages_policy,
		   debug_policy = EXCLUDED.debug_policy,
		   updated_at = EXCLUDED.updated_at`,
		policy.NetworkID, policy.GroupName, policy.MessagesPolicy, policy.DebugPolicy, policy.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) GroupPolicyGet(ctx context.Context, networkID, groupName string) (*GroupPolicy, error) {
	var policy GroupPolicy
	err := s.pool.QueryRow(ctx,
		`SELECT network_id, group_name, messages_policy, debug_policy, updated_at
		 FROM group_policies
		 WHERE network_id = $1 AND group_name = $2`,
		networkID, groupName,
	).Scan(&policy.NetworkID, &policy.GroupName, &policy.MessagesPolicy, &policy.DebugPolicy, &policy.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

// ---------------------------------------------------------------------------
// Nodes
// ---------------------------------------------------------------------------

func (s *PostgresStore) NodeRegister(ctx context.Context, node NodeRecord) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO networks (network_id, created_at) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		node.NetworkID, time.Now().UTC(),
	); err != nil {
		return err
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO nodes (network_id, name, token, peer_url, github_id, owner_subject, authorized_by, enrollment_id, authorized_at, last_seen_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (network_id, name) DO UPDATE SET
		   token = EXCLUDED.token,
		   peer_url = EXCLUDED.peer_url,
		   github_id = EXCLUDED.github_id,
		   owner_subject = EXCLUDED.owner_subject,
		   authorized_by = EXCLUDED.authorized_by,
		   enrollment_id = EXCLUDED.enrollment_id,
		   last_seen_at = EXCLUDED.last_seen_at`,
		node.NetworkID, node.Name, node.Token, node.PeerURL, node.GitHubID, node.OwnerSubject, node.AuthorizedBy, node.EnrollmentID, node.AuthorizedAt, node.LastSeenAt,
	)
	return err
}

func (s *PostgresStore) NodeList(ctx context.Context, networkID string) ([]NodeRecord, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT network_id, name, token, peer_url, github_id, owner_subject, authorized_by, enrollment_id, authorized_at, last_seen_at FROM nodes WHERE network_id = $1 ORDER BY name",
		networkID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []NodeRecord
	for rows.Next() {
		var n NodeRecord
		if err := rows.Scan(&n.NetworkID, &n.Name, &n.Token, &n.PeerURL, &n.GitHubID, &n.OwnerSubject, &n.AuthorizedBy, &n.EnrollmentID, &n.AuthorizedAt, &n.LastSeenAt); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s *PostgresStore) NodeListAll(ctx context.Context) ([]NodeRecord, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT network_id, name, token, peer_url, github_id, owner_subject, authorized_by, enrollment_id, authorized_at, last_seen_at FROM nodes ORDER BY network_id, name",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []NodeRecord
	for rows.Next() {
		var n NodeRecord
		if err := rows.Scan(&n.NetworkID, &n.Name, &n.Token, &n.PeerURL, &n.GitHubID, &n.OwnerSubject, &n.AuthorizedBy, &n.EnrollmentID, &n.AuthorizedAt, &n.LastSeenAt); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s *PostgresStore) NodeGet(ctx context.Context, networkID, name string) (*NodeRecord, error) {
	var n NodeRecord
	err := s.pool.QueryRow(ctx,
		"SELECT network_id, name, token, peer_url, github_id, owner_subject, authorized_by, enrollment_id, authorized_at, last_seen_at FROM nodes WHERE network_id = $1 AND name = $2",
		networkID, name,
	).Scan(&n.NetworkID, &n.Name, &n.Token, &n.PeerURL, &n.GitHubID, &n.OwnerSubject, &n.AuthorizedBy, &n.EnrollmentID, &n.AuthorizedAt, &n.LastSeenAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *PostgresStore) NodeGetByToken(ctx context.Context, token string) (*NodeRecord, error) {
	var n NodeRecord
	err := s.pool.QueryRow(ctx,
		"SELECT network_id, name, token, peer_url, github_id, owner_subject, authorized_by, enrollment_id, authorized_at, last_seen_at FROM nodes WHERE token = $1",
		token,
	).Scan(&n.NetworkID, &n.Name, &n.Token, &n.PeerURL, &n.GitHubID, &n.OwnerSubject, &n.AuthorizedBy, &n.EnrollmentID, &n.AuthorizedAt, &n.LastSeenAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *PostgresStore) NodeDelete(ctx context.Context, networkID, name string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM nodes WHERE network_id = $1 AND name = $2", networkID, name)
	return err
}

func (s *PostgresStore) NodeUpdateLastSeen(ctx context.Context, networkID, name string) error {
	_, err := s.pool.Exec(ctx, "UPDATE nodes SET last_seen_at = $1 WHERE network_id = $2 AND name = $3", time.Now().UTC(), networkID, name)
	return err
}

// ---------------------------------------------------------------------------
// Enrollments
// ---------------------------------------------------------------------------

func (s *PostgresStore) NodeEnrollmentCreate(ctx context.Context, enrollment NodeEnrollment) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO networks (network_id, created_at) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		enrollment.NetworkID, time.Now().UTC(),
	); err != nil {
		return err
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO node_enrollments (id, network_id, owner_subject, issued_by, node_name, token_hash, uses_remaining, expires_at, created_at, redeemed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		enrollment.ID, enrollment.NetworkID, enrollment.OwnerSubject, enrollment.IssuedBy, enrollment.NodeName, enrollment.TokenHash, enrollment.UsesRemaining, enrollment.ExpiresAt, enrollment.CreatedAt, enrollment.RedeemedAt,
	)
	return err
}

func (s *PostgresStore) NodeEnrollmentGetByTokenHash(ctx context.Context, tokenHash string) (*NodeEnrollment, error) {
	var e NodeEnrollment
	err := s.pool.QueryRow(ctx,
		`SELECT id, network_id, owner_subject, issued_by, node_name, token_hash, uses_remaining, expires_at, created_at, redeemed_at
		 FROM node_enrollments WHERE token_hash = $1`,
		tokenHash,
	).Scan(&e.ID, &e.NetworkID, &e.OwnerSubject, &e.IssuedBy, &e.NodeName, &e.TokenHash, &e.UsesRemaining, &e.ExpiresAt, &e.CreatedAt, &e.RedeemedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// NodeEnrollmentConsume atomically decrements uses_remaining and returns the
// updated enrollment. Returns nil if no valid enrollment exists.
func (s *PostgresStore) NodeEnrollmentConsume(ctx context.Context, tokenHash string, redeemedAt time.Time) (*NodeEnrollment, error) {
	var e NodeEnrollment
	err := s.pool.QueryRow(ctx,
		`UPDATE node_enrollments
		 SET uses_remaining = uses_remaining - 1,
		     redeemed_at = CASE WHEN uses_remaining = 1 THEN $1 ELSE redeemed_at END
		 WHERE token_hash = $2 AND uses_remaining > 0 AND expires_at > $1
		 RETURNING id, network_id, owner_subject, issued_by, node_name, token_hash, uses_remaining, expires_at, created_at, redeemed_at`,
		redeemedAt, tokenHash,
	).Scan(&e.ID, &e.NetworkID, &e.OwnerSubject, &e.IssuedBy, &e.NodeName, &e.TokenHash, &e.UsesRemaining, &e.ExpiresAt, &e.CreatedAt, &e.RedeemedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// ---------------------------------------------------------------------------
// Device Codes
// ---------------------------------------------------------------------------

func (s *PostgresStore) DeviceCodeCreate(ctx context.Context, dc DeviceCode) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO device_codes (code, public_key, node_name, status, created_at, expires_at) VALUES ($1, $2, $3, $4, $5, $6)",
		dc.Code, dc.PublicKey, dc.NodeName, dc.Status, dc.CreatedAt, dc.ExpiresAt,
	)
	return err
}

func (s *PostgresStore) DeviceCodeGet(ctx context.Context, code string) (*DeviceCode, error) {
	var dc DeviceCode
	err := s.pool.QueryRow(ctx,
		"SELECT code, public_key, node_name, status, created_at, expires_at FROM device_codes WHERE code = $1 AND expires_at > $2",
		code, time.Now().UTC(),
	).Scan(&dc.Code, &dc.PublicKey, &dc.NodeName, &dc.Status, &dc.CreatedAt, &dc.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &dc, nil
}

func (s *PostgresStore) DeviceCodeConfirm(ctx context.Context, code string) error {
	res, err := s.pool.Exec(ctx,
		"UPDATE device_codes SET status = 'authorized' WHERE code = $1 AND status = 'pending' AND expires_at > $2",
		code, time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("device code not found or already confirmed")
	}
	return nil
}

func (s *PostgresStore) DeviceCodeCleanup(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM device_codes WHERE expires_at < $1", time.Now().UTC())
	return err
}

// ---------------------------------------------------------------------------
// GitHub App (singleton)
// ---------------------------------------------------------------------------

func (s *PostgresStore) GitHubAppGet(ctx context.Context) (*GitHubApp, error) {
	var app GitHubApp
	err := s.pool.QueryRow(ctx,
		"SELECT app_id, client_id, client_secret, pem, webhook_secret, owner, created_at FROM github_app WHERE id = 1",
	).Scan(&app.AppID, &app.ClientID, &app.ClientSecret, &app.PEM, &app.WebhookSecret, &app.Owner, &app.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &app, nil
}

func (s *PostgresStore) GitHubAppSet(ctx context.Context, app GitHubApp) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO github_app (id, app_id, client_id, client_secret, pem, webhook_secret, owner, created_at)
		 VALUES (1, $1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (id) DO UPDATE SET
		   app_id = EXCLUDED.app_id,
		   client_id = EXCLUDED.client_id,
		   client_secret = EXCLUDED.client_secret,
		   pem = EXCLUDED.pem,
		   webhook_secret = EXCLUDED.webhook_secret,
		   owner = EXCLUDED.owner`,
		app.AppID, app.ClientID, app.ClientSecret, app.PEM, app.WebhookSecret, app.Owner, app.CreatedAt,
	)
	return err
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func (s *PostgresStore) UserUpsert(ctx context.Context, user User) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (github_id, username, avatar_url, created_at, last_login_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (github_id) DO UPDATE SET
		   username = EXCLUDED.username,
		   avatar_url = EXCLUDED.avatar_url,
		   last_login_at = EXCLUDED.last_login_at`,
		user.GitHubID, user.Username, user.AvatarURL, user.CreatedAt, user.LastLoginAt,
	)
	return err
}

func (s *PostgresStore) UserGetByID(ctx context.Context, githubID int64) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		"SELECT github_id, username, avatar_url, created_at, last_login_at FROM users WHERE github_id = $1",
		githubID,
	).Scan(&u.GitHubID, &u.Username, &u.AvatarURL, &u.CreatedAt, &u.LastLoginAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *PostgresStore) UserGetByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	err := s.pool.QueryRow(ctx,
		"SELECT github_id, username, avatar_url, created_at, last_login_at FROM users WHERE username = $1",
		username,
	).Scan(&u.GitHubID, &u.Username, &u.AvatarURL, &u.CreatedAt, &u.LastLoginAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func (s *PostgresStore) SessionCreate(ctx context.Context, sess Session) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO sessions (token, github_id, created_at, expires_at) VALUES ($1, $2, $3, $4)",
		sess.Token, sess.GitHubID, sess.CreatedAt, sess.ExpiresAt,
	)
	return err
}

func (s *PostgresStore) SessionGet(ctx context.Context, token string) (*Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx,
		"SELECT token, github_id, created_at, expires_at FROM sessions WHERE token = $1 AND expires_at > $2",
		token, time.Now().UTC(),
	).Scan(&sess.Token, &sess.GitHubID, &sess.CreatedAt, &sess.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *PostgresStore) SessionDelete(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE token = $1", token)
	return err
}

func (s *PostgresStore) SessionDeleteByUser(ctx context.Context, githubID int64) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE github_id = $1", githubID)
	return err
}

// ---------------------------------------------------------------------------
// OAuth State
// ---------------------------------------------------------------------------

func (s *PostgresStore) OAuthStateCreate(ctx context.Context, state OAuthState) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO oauth_state (state, created_at, expires_at) VALUES ($1, $2, $3)",
		state.State, state.CreatedAt, state.ExpiresAt,
	)
	return err
}

func (s *PostgresStore) OAuthStateConsume(ctx context.Context, state string) error {
	res, err := s.pool.Exec(ctx,
		"DELETE FROM oauth_state WHERE state = $1 AND expires_at > $2",
		state, time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("oauth state not found or expired")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Invites
// ---------------------------------------------------------------------------

func (s *PostgresStore) InviteCreate(ctx context.Context, invite Invite) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO networks (network_id, created_at) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`,
		invite.NetworkID, time.Now().UTC(),
	); err != nil {
		return err
	}

	_, err := s.pool.Exec(ctx,
		"INSERT INTO invites (network_id, token, created_by, uses_remaining, expires_at, created_at) VALUES ($1, $2, $3, $4, $5, $6)",
		invite.NetworkID, invite.Token, invite.CreatedBy, invite.UsesRemaining, invite.ExpiresAt, invite.CreatedAt,
	)
	return err
}

func (s *PostgresStore) InviteGet(ctx context.Context, token string) (*Invite, error) {
	var inv Invite
	err := s.pool.QueryRow(ctx,
		"SELECT network_id, token, created_by, uses_remaining, expires_at, created_at FROM invites WHERE token = $1 AND expires_at > $2",
		token, time.Now().UTC(),
	).Scan(&inv.NetworkID, &inv.Token, &inv.CreatedBy, &inv.UsesRemaining, &inv.ExpiresAt, &inv.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func (s *PostgresStore) InviteConsume(ctx context.Context, token string) error {
	now := time.Now().UTC()

	res, err := s.pool.Exec(ctx,
		"UPDATE invites SET uses_remaining = uses_remaining - 1 WHERE token = $1 AND uses_remaining > 0 AND expires_at > $2",
		token, now,
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("invite not found, expired, or no uses remaining")
	}

	// If uses_remaining reached 0, delete the row.
	s.pool.Exec(ctx, "DELETE FROM invites WHERE token = $1 AND uses_remaining <= 0", token)
	return nil
}

func (s *PostgresStore) InviteList(ctx context.Context, networkID string) ([]Invite, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT network_id, token, created_by, uses_remaining, expires_at, created_at FROM invites WHERE network_id = $1 AND expires_at > $2 ORDER BY created_at",
		networkID, time.Now().UTC(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []Invite
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.NetworkID, &inv.Token, &inv.CreatedBy, &inv.UsesRemaining, &inv.ExpiresAt, &inv.CreatedAt); err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

func (s *PostgresStore) InviteDelete(ctx context.Context, networkID, token string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM invites WHERE network_id = $1 AND token = $2", networkID, token)
	return err
}

// ---------------------------------------------------------------------------
// Access Grants
// ---------------------------------------------------------------------------

func (s *PostgresStore) AccessGrantCreate(ctx context.Context, grant AccessGrant) error {
	verbsJSON, err := json.Marshal(grant.Verbs)
	if err != nil {
		return err
	}

	var sessionID any
	if grant.SessionID != nil {
		sessionID = int64(*grant.SessionID)
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO access_grants (id, network_id, target_node, session_id, session_name, verbs, audience_subject_kind, audience_subject_id, audience_display, issued_by, created_at, expires_at, revoked_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		grant.ID, grant.NetworkID, grant.TargetNode, sessionID, grant.SessionName, string(verbsJSON), grant.AudienceSubjectKind, grant.AudienceSubjectID, grant.AudienceDisplay, grant.IssuedBy, grant.CreatedAt, grant.ExpiresAt, grant.RevokedAt,
	)
	return err
}

func (s *PostgresStore) AccessGrantGet(ctx context.Context, networkID, grantID string) (*AccessGrant, error) {
	var (
		grant       AccessGrant
		sessionID   *int64
		verbsJSON   string
		revokedAt   *time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT id, network_id, target_node, session_id, session_name, verbs, audience_subject_kind, audience_subject_id, audience_display, issued_by, created_at, expires_at, revoked_at
		 FROM access_grants WHERE network_id = $1 AND id = $2`,
		networkID, grantID,
	).Scan(
		&grant.ID, &grant.NetworkID, &grant.TargetNode,
		&sessionID, &grant.SessionName, &verbsJSON,
		&grant.AudienceSubjectKind, &grant.AudienceSubjectID, &grant.AudienceDisplay,
		&grant.IssuedBy, &grant.CreatedAt, &grant.ExpiresAt, &revokedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sessionID != nil {
		id := uint32(*sessionID)
		grant.SessionID = &id
	}
	if revokedAt != nil {
		grant.RevokedAt = revokedAt
	}
	if strings.TrimSpace(verbsJSON) != "" {
		if err := json.Unmarshal([]byte(verbsJSON), &grant.Verbs); err != nil {
			return nil, err
		}
	}
	return &grant, nil
}

func (s *PostgresStore) AccessGrantList(ctx context.Context, networkID string, filter AccessGrantFilter) ([]AccessGrant, error) {
	query := `SELECT id, network_id, target_node, session_id, session_name, verbs, audience_subject_kind, audience_subject_id, audience_display, issued_by, created_at, expires_at, revoked_at
		FROM access_grants WHERE network_id = $1`
	args := []any{networkID}
	paramN := 2

	if strings.TrimSpace(filter.TargetNode) != "" {
		query += fmt.Sprintf(" AND target_node = $%d", paramN)
		args = append(args, strings.TrimSpace(filter.TargetNode))
		paramN++
	}
	if strings.TrimSpace(filter.AudienceSubjectID) != "" {
		query += fmt.Sprintf(" AND audience_subject_id = $%d", paramN)
		args = append(args, strings.TrimSpace(filter.AudienceSubjectID))
		paramN++
	}
	if filter.ActiveOnly {
		query += fmt.Sprintf(" AND expires_at > $%d AND revoked_at IS NULL", paramN)
		args = append(args, time.Now().UTC())
		paramN++
	}
	query += " ORDER BY created_at DESC"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []AccessGrant
	for rows.Next() {
		var (
			grant     AccessGrant
			sessionID *int64
			verbsJSON string
			revokedAt *time.Time
		)
		if err := rows.Scan(
			&grant.ID, &grant.NetworkID, &grant.TargetNode,
			&sessionID, &grant.SessionName, &verbsJSON,
			&grant.AudienceSubjectKind, &grant.AudienceSubjectID, &grant.AudienceDisplay,
			&grant.IssuedBy, &grant.CreatedAt, &grant.ExpiresAt, &revokedAt,
		); err != nil {
			return nil, err
		}
		if sessionID != nil {
			id := uint32(*sessionID)
			grant.SessionID = &id
		}
		if revokedAt != nil {
			grant.RevokedAt = revokedAt
		}
		if strings.TrimSpace(verbsJSON) != "" {
			if err := json.Unmarshal([]byte(verbsJSON), &grant.Verbs); err != nil {
				return nil, err
			}
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

func (s *PostgresStore) AccessGrantRevoke(ctx context.Context, networkID, grantID string, revokedAt time.Time) error {
	res, err := s.pool.Exec(ctx,
		"UPDATE access_grants SET revoked_at = $1 WHERE network_id = $2 AND id = $3 AND revoked_at IS NULL",
		revokedAt, networkID, grantID,
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("access grant not found")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Revoked Keys
// ---------------------------------------------------------------------------

func (s *PostgresStore) RevokedKeyAdd(ctx context.Context, key RevokedKey) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO revoked_keys (public_key, revoked_at, reason) VALUES ($1, $2, $3)
		 ON CONFLICT (public_key) DO UPDATE SET revoked_at = EXCLUDED.revoked_at, reason = EXCLUDED.reason`,
		key.PublicKey, key.RevokedAt, key.Reason,
	)
	return err
}

func (s *PostgresStore) RevokedKeyCheck(ctx context.Context, publicKey string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM revoked_keys WHERE public_key = $1", publicKey).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ---------------------------------------------------------------------------
// OIDC Users
// ---------------------------------------------------------------------------

func (s *PostgresStore) OIDCUserUpsert(ctx context.Context, user OIDCUser) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO oidc_users (sub, username, avatar_url, created_at, last_login_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (sub) DO UPDATE SET
		   username = EXCLUDED.username,
		   avatar_url = EXCLUDED.avatar_url,
		   last_login_at = EXCLUDED.last_login_at`,
		user.Sub, user.Username, user.AvatarURL, user.CreatedAt, user.LastLoginAt,
	)
	return err
}

func (s *PostgresStore) OIDCUserGetBySub(ctx context.Context, sub string) (*OIDCUser, error) {
	var u OIDCUser
	err := s.pool.QueryRow(ctx,
		"SELECT sub, username, avatar_url, created_at, last_login_at FROM oidc_users WHERE sub = $1",
		sub,
	).Scan(&u.Sub, &u.Username, &u.AvatarURL, &u.CreatedAt, &u.LastLoginAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *PostgresStore) OIDCUserListByUsername(ctx context.Context, username string) ([]OIDCUser, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT sub, username, avatar_url, created_at, last_login_at FROM oidc_users WHERE username = $1 ORDER BY sub",
		username,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []OIDCUser
	for rows.Next() {
		var u OIDCUser
		if err := rows.Scan(&u.Sub, &u.Username, &u.AvatarURL, &u.CreatedAt, &u.LastLoginAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ---------------------------------------------------------------------------
// OIDC Sessions
// ---------------------------------------------------------------------------

func (s *PostgresStore) OIDCSessionCreate(ctx context.Context, sess OIDCSession) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO oidc_sessions (token, sub, created_at, expires_at) VALUES ($1, $2, $3, $4)",
		sess.Token, sess.Sub, sess.CreatedAt, sess.ExpiresAt,
	)
	return err
}

func (s *PostgresStore) OIDCSessionGet(ctx context.Context, token string) (*OIDCSession, error) {
	var sess OIDCSession
	err := s.pool.QueryRow(ctx,
		"SELECT token, sub, created_at, expires_at FROM oidc_sessions WHERE token = $1 AND expires_at > $2",
		token, time.Now().UTC(),
	).Scan(&sess.Token, &sess.Sub, &sess.CreatedAt, &sess.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *PostgresStore) OIDCSessionDelete(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM oidc_sessions WHERE token = $1", token)
	return err
}

// ---------------------------------------------------------------------------
// OIDC Device Flows
// ---------------------------------------------------------------------------

func (s *PostgresStore) OIDCDeviceFlowCreate(ctx context.Context, flow OIDCDeviceFlow) error {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO oidc_device_flows (poll_token, device_code, network_id, node_name, node_token, expires_at) VALUES ($1, $2, $3, $4, $5, $6)",
		flow.PollToken, flow.DeviceCode, flow.NetworkID, flow.NodeName, flow.NodeToken, flow.ExpiresAt,
	)
	return err
}

func (s *PostgresStore) OIDCDeviceFlowGet(ctx context.Context, pollToken string) (*OIDCDeviceFlow, error) {
	var flow OIDCDeviceFlow
	err := s.pool.QueryRow(ctx,
		"SELECT poll_token, device_code, network_id, node_name, node_token, expires_at FROM oidc_device_flows WHERE poll_token = $1 AND expires_at > $2",
		pollToken, time.Now().UTC(),
	).Scan(&flow.PollToken, &flow.DeviceCode, &flow.NetworkID, &flow.NodeName, &flow.NodeToken, &flow.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &flow, nil
}

func (s *PostgresStore) OIDCDeviceFlowComplete(ctx context.Context, pollToken, nodeToken string) error {
	res, err := s.pool.Exec(ctx,
		"UPDATE oidc_device_flows SET node_token = $1 WHERE poll_token = $2 AND expires_at > $3",
		nodeToken, pollToken, time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("oidc device flow not found or expired")
	}
	return nil
}
