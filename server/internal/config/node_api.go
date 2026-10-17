package config

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

// This process does not load App signing/encryption keys or wallet settings.
type NodeAPI struct {
	Enabled                  bool
	WireGuardManifestEnabled bool
	// 流量上报入库与权益扣减，默认关闭。关闭时该路由不注册，节点上报一律 404。
	TrafficReportEnabled            bool
	Env                             Env
	Listen                          string
	DatabaseURL                     string
	ServerCert, ServerKey, ClientCA string
	// 节点注册（一次性令牌换证书），默认关闭。打开必须同时给出签发 CA 的证书与私钥绝对路径。
	// 关闭时 /node/v1/enroll 根本不注册，监听器继续强制客户端证书。
	EnrollEnabled             bool
	EnrollCACert, EnrollCAKey string
}

func LoadNodeAPI() (NodeAPI, error) {
	c := NodeAPI{Env: Env(get("APP_ENV", "development")), Listen: get("NODE_API_LISTEN", "127.0.0.1:8443"), DatabaseURL: os.Getenv("NODE_DATABASE_URL"), ServerCert: os.Getenv("NODE_TLS_SERVER_CERT"), ServerKey: os.Getenv("NODE_TLS_SERVER_KEY"), ClientCA: os.Getenv("NODE_TLS_CLIENT_CA")}
	var err error
	if c.Enabled, err = strictBool("NODE_API_ENABLED", false); err != nil {
		return c, err
	}
	if c.Env != EnvDevelopment && c.Env != EnvStaging && c.Env != EnvProduction {
		return c, errors.New("APP_ENV 非法")
	}
	if c.WireGuardManifestEnabled, err = strictBool("NODE_WG_MANIFEST_API_ENABLED", false); err != nil {
		return c, err
	}
	if c.TrafficReportEnabled, err = strictBool("NODE_TRAFFIC_API_ENABLED", false); err != nil {
		return c, err
	}
	if c.EnrollEnabled, err = strictBool("NODE_ENROLL_ENABLED", false); err != nil {
		return c, err
	}
	c.EnrollCACert, c.EnrollCAKey = os.Getenv("NODE_ENROLL_CA_CERT"), os.Getenv("NODE_ENROLL_CA_KEY")
	if !c.Enabled {
		return c, nil
	}
	if c.DatabaseURL == "" {
		return c, errors.New("必须设置独立节点控制面 NODE_DATABASE_URL")
	}
	host, port, err := net.SplitHostPort(c.Listen)
	n, e := strconv.Atoi(port)
	if err != nil || e != nil || n < 1 || n > 65535 || net.ParseIP(host) == nil {
		return c, errors.New("NODE_API_LISTEN 必须明确指定监听IP和有效端口")
	}
	if !filepath.IsAbs(c.ServerCert) || !filepath.IsAbs(c.ServerKey) || !filepath.IsAbs(c.ClientCA) {
		return c, errors.New("必须设置节点TLS证书、密钥及专用客户端CA的绝对路径")
	}
	if c.EnrollEnabled && (!filepath.IsAbs(c.EnrollCACert) || !filepath.IsAbs(c.EnrollCAKey)) {
		return c, errors.New("启用节点注册必须设置 NODE_ENROLL_CA_CERT 与 NODE_ENROLL_CA_KEY 的绝对路径")
	}
	if !c.EnrollEnabled {
		// 关闭时不允许残留路径，避免「以为开着其实没开」或反之。
		c.EnrollCACert, c.EnrollCAKey = "", ""
	}
	return c, nil
}
