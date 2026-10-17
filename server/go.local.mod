module github.com/yuhai-vpn/server

go 1.23

require (
	github.com/gin-gonic/gin v1.10.0
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.7.1
	github.com/redis/go-redis/v9 v9.7.0
	golang.org/x/crypto v0.27.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/gabriel-vasile/mimetype v1.4.3 // indirect
	github.com/gin-contrib/sse v0.1.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.20.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/pelletier/go-toml/v2 v2.2.2 // indirect
	github.com/ugorji/go/codec v1.2.12 // indirect
	golang.org/x/net v0.25.0 // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/sys v0.25.0 // indirect
	golang.org/x/text v0.18.0 // indirect
	google.golang.org/protobuf v1.34.1 // indirect
)

replace (
	golang.org/x/arch => ../../../third_party/arch
	golang.org/x/crypto => ../../../third_party/crypto
	golang.org/x/net => ../../../third_party/net
	golang.org/x/sync => ../../../third_party/sync
	golang.org/x/sys => ../../../third_party/sys
	golang.org/x/text => ../../../third_party/text
	golang.org/x/time => ../../../third_party/time
	google.golang.org/protobuf => ../../../third_party/protobuf
	gopkg.in/check.v1 => ../../../third_party/check.v1
	gopkg.in/yaml.v3 => ../../../third_party/yaml.v3
)
