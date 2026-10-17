-- +goose Up
ALTER TABLE node_heartbeats ADD COLUMN ready_protocols JSONB NOT NULL DEFAULT '[]'::jsonb
  CHECK (jsonb_typeof(ready_protocols) = 'array' AND jsonb_array_length(ready_protocols) <= 5
    AND ready_protocols <@ '["VLESS_REALITY","TROJAN","SHADOWSOCKS_2022","WIREGUARD","HYSTERIA2"]'::jsonb);

-- +goose Down
ALTER TABLE node_heartbeats DROP COLUMN ready_protocols;
