-- +goose Up
-- 对外下载的当前安装包。和 app_versions 的草稿审批分开：
-- 构建流水线用一次性令牌发布，后台可以查看并切回上一份。
CREATE TABLE app_releases (
  id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  platform             TEXT         NOT NULL CHECK (platform IN ('windows', 'android')),
  version              VARCHAR(32)  NOT NULL,
  build                INT          NOT NULL CHECK (build > 0),
  url                  VARCHAR(512) NOT NULL,
  sha256               CHAR(64)     NOT NULL,
  size_bytes           BIGINT       NOT NULL CHECK (size_bytes > 0),
  released_at          TIMESTAMPTZ  NOT NULL,
  min_supported_build  INT          NOT NULL CHECK (min_supported_build > 0 AND min_supported_build <= build),
  notes                TEXT         NOT NULL DEFAULT '',
  cert_sha256          CHAR(64),
  is_current           BOOLEAN      NOT NULL DEFAULT false,
  created_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
  UNIQUE (platform, build)
);
CREATE UNIQUE INDEX app_releases_one_current ON app_releases (platform) WHERE is_current;

-- +goose Down
DROP INDEX IF EXISTS app_releases_one_current;
DROP TABLE IF EXISTS app_releases;
