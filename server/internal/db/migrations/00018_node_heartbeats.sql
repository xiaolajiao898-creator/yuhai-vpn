-- +goose Up
-- One durable latest receipt per node, not an unbounded telemetry log.
-- Certificate enrollment/rotation must preserve the sequence watermark.
CREATE TABLE node_heartbeats (
  node_id BIGINT PRIMARY KEY REFERENCES nodes(id),
  sequence BIGINT NOT NULL CHECK (sequence > 0),
  payload_hash CHAR(64) NOT NULL CHECK (payload_hash ~ '^[a-f0-9]{64}$'),
  sent_at TIMESTAMPTZ NOT NULL,
  received_at TIMESTAMPTZ NOT NULL,
  config_version BIGINT NOT NULL CHECK (config_version >= 0),
  snapshot_checksum VARCHAR(64) NOT NULL CHECK (snapshot_checksum = '' OR snapshot_checksum ~ '^[a-f0-9]{64}$'),
  cpu SMALLINT NOT NULL CHECK (cpu BETWEEN 0 AND 100),
  mem SMALLINT NOT NULL CHECK (mem BETWEEN 0 AND 100),
  net_rx_bps BIGINT NOT NULL CHECK (net_rx_bps >= 0),
  net_tx_bps BIGINT NOT NULL CHECK (net_tx_bps >= 0),
  kernel VARCHAR(64)
);
-- No node is enrolled, enabled, or granted an administrative permission here.

-- +goose Down
DROP TABLE node_heartbeats;
