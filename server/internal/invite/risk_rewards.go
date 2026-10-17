package invite

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

func rewardRisk(ctx context.Context, tx pgx.Tx, o rewardOrder, r rewardRelation, now time.Time) (string, []string, error) {
	status, flags := "PENDING", []string{}
	if o.Status != "COMPLETED" || o.Fulfillment != "FULFILLED" || o.Base <= 0 || o.Pay < o.Base {
		return "CANCELLED", []string{"ORDER_ABNORMAL"}, nil
	}
	if o.PaidAt == nil || o.CompletedAt == nil || o.PaidAt.After(now) || o.CompletedAt.After(now) || o.CompletedAt.Before(*o.PaidAt) {
		status, flags = reviewReward(status, flags, "PAYMENT_TIME_UNCERTAIN")
	}
	if o.PaidAt != nil && r.Created.After(*o.PaidAt) {
		return "CANCELLED", []string{"RELATION_AFTER_PAYMENT"}, nil
	}
	if r.Inviter == r.Invitee {
		return "CANCELLED", []string{"SELF_INVITE"}, nil
	}
	var flagged []any
	if json.Unmarshal(r.Flags, &flagged) != nil || len(flagged) > 0 {
		status, flags = reviewReward(status, flags, "RELATION_RISK")
	}
	type user struct {
		ID             int64
		Status, Prefix string
		Created        time.Time
		Risk           []byte
	}
	rows, err := tx.Query(ctx, `SELECT id,status,COALESCE(register_ip_prefix_hash,''),created_at,risk_flags FROM users WHERE id=ANY($1::bigint[]) ORDER BY id FOR SHARE`, []int64{r.Inviter, r.Invitee})
	if err != nil {
		return "", nil, err
	}
	users := []user{}
	for rows.Next() {
		var u user
		if err = rows.Scan(&u.ID, &u.Status, &u.Prefix, &u.Created, &u.Risk); err != nil {
			rows.Close()
			return "", nil, err
		}
		users = append(users, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", nil, err
	}
	if len(users) != 2 {
		return "CANCELLED", []string{"ACCOUNT_MISSING"}, nil
	}
	for _, u := range users {
		if u.Status == "BANNED" {
			return "CANCELLED", []string{"ACCOUNT_BANNED"}, nil
		}
		var risk []any
		if u.Status != "ACTIVE" || json.Unmarshal(u.Risk, &risk) != nil || len(risk) > 0 {
			status, flags = reviewReward(status, flags, "ACCOUNT_RISK")
		}
	}
	if users[0].Prefix != "" && users[0].Prefix == users[1].Prefix && users[1].Created.After(users[0].Created.Add(-time.Hour)) && users[1].Created.Before(users[0].Created.Add(time.Hour)) {
		status, flags = reviewReward(status, flags, "CLOSE_REGISTRATION_IP")
	}
	var fingerprint bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices a JOIN devices b ON b.user_id=$2 WHERE a.user_id=$1 AND ((NULLIF(a.fingerprint_hash,'') IS NOT NULL AND a.fingerprint_hash=b.fingerprint_hash) OR (NULLIF(a.pubkey,'') IS NOT NULL AND a.pubkey=b.pubkey)))`, r.Inviter, r.Invitee).Scan(&fingerprint); err != nil {
		return "", nil, err
	}
	if fingerprint {
		return "CANCELLED", []string{"SHARED_DEVICE"}, nil
	}
	var blacklisted bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM blacklist b WHERE b.status='ACTIVE' AND (b.expires_at IS NULL OR b.expires_at>$3) AND (
	 (b.target_type='USER' AND (b.target_value=ANY($4::text[]) OR b.target_value IN(SELECT account_no FROM users WHERE id=ANY($6::bigint[]))))
	 OR (b.target_type='INVITE_CODE' AND (b.target_value=$5 OR b.target_value IN(SELECT invite_code FROM users WHERE id=ANY($6::bigint[]))))
	 OR (b.target_type='IP_PREFIX' AND b.target_value IN(SELECT register_ip_prefix_hash FROM users WHERE id=ANY($6::bigint[])))
	 OR (b.target_type='DEVICE' AND b.target_value IN(SELECT id::text FROM devices WHERE user_id=ANY($6::bigint[]) UNION ALL SELECT device_uid::text FROM devices WHERE user_id=ANY($6::bigint[])))
	 OR (b.target_type='FINGERPRINT' AND b.target_value IN(SELECT fingerprint_hash FROM devices WHERE user_id=ANY($6::bigint[])))
	 OR (b.target_type='PAY_ADDRESS' AND b.target_value IN(SELECT t.from_address FROM chain_transfers t JOIN orders o ON o.chain_transfer_id=t.id WHERE o.user_id IN($1,$2)))
	))`, r.Inviter, r.Invitee, now, []string{strconv.FormatInt(r.Inviter, 10), strconv.FormatInt(r.Invitee, 10)}, r.Code, []int64{r.Inviter, r.Invitee}).Scan(&blacklisted); err != nil {
		return "", nil, err
	}
	if blacklisted {
		return "CANCELLED", []string{"BLACKLISTED"}, nil
	}
	if o.Method == "BALANCE" {
		return reviewWithNoError(status, flags, "BALANCE_PAYMENT_REVIEW")
	}
	if o.Method != "USDT_TRC20" || o.TransferID == nil {
		return reviewWithNoError(status, flags, "PAYMENT_EVIDENCE_MISSING")
	}
	var network, confirmation, match, from string
	var linked, amount int64
	var aligned, deposit, exception bool
	err = tx.QueryRow(ctx, `SELECT t.network,t.confirm_status,t.match_status,COALESCE(t.order_id,0),t.amount_micro,t.from_address,
	 (t.receive_address_id=a.id AND t.to_address=a.address AND t.network=a.network),
	 EXISTS(SELECT 1 FROM balance_ledger l WHERE l.user_id=$3 AND l.source_type='CHAIN_TRANSFER' AND l.source_id=t.id AND l.entry_type='DEPOSIT' AND l.amount_micro=t.amount_micro),
	 EXISTS(SELECT 1 FROM deposit_exceptions e WHERE e.chain_transfer_id=t.id)
	 FROM chain_transfers t JOIN orders source ON source.id=$2 JOIN receive_addresses a ON a.id=source.receive_address_id WHERE t.id=$1 FOR SHARE OF t`, *o.TransferID, o.ID, o.UserID).Scan(&network, &confirmation, &match, &linked, &amount, &from, &aligned, &deposit, &exception)
	if errors.Is(err, pgx.ErrNoRows) {
		return reviewWithNoError(status, flags, "PAYMENT_EVIDENCE_MISSING")
	}
	if err != nil {
		return "", nil, err
	}
	if exception || confirmation == "FAILED" || match == "EXCEPTION" {
		return "CANCELLED", []string{"ORDER_ABNORMAL"}, nil
	}
	if network != "TRON" || confirmation != "SOLIDIFIED" || match != "MATCHED" || linked != o.ID || amount != o.Pay || !aligned || !deposit || from == "" {
		return reviewWithNoError(status, flags, "PAYMENT_EVIDENCE_UNVERIFIED")
	}
	var shared, public bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM chain_transfers t JOIN orders o ON o.chain_transfer_id=t.id WHERE o.user_id=$1 AND t.from_address=$2), EXISTS(SELECT 1 FROM shared_pay_addresses WHERE address=$2)`, r.Inviter, from).Scan(&shared, &public); err != nil {
		return "", nil, err
	}
	// Public exchange addresses are not proof of self-invitation. Keep them for
	// review rather than permanently cancelling a legitimate customer reward.
	if shared || public {
		status, flags = reviewReward(status, flags, "SHARED_PAY_ADDRESS")
	}
	return status, flags, nil
}

func reviewWithNoError(status string, flags []string, reason string) (string, []string, error) {
	status, flags = reviewReward(status, flags, reason)
	return status, flags, nil
}
