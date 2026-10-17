package com.yuhai.yuhai_vpn

import org.junit.Assert.*
import org.junit.Test
import java.time.Instant

class XrayPolicyTest {
    private val now = Instant.parse("2026-09-28T01:00:00Z").toEpochMilli()

    // 与服务端 egress.ClientConfig(ModeTUN) 生成的形状一致（精简了路由网段）。
    private val config = """{"dns":{"queryStrategy":"UseIPv4","servers":["https://1.1.1.1/dns-query"]},
        "inbounds":[{"port":0,"protocol":"tun","settings":{"mtu":1500,"name":"xray0"},"tag":"tun-in"}],
        "log":{"loglevel":"warning"},
        "outbounds":[{"protocol":"trojan","settings":{"address":"example.com","port":443,"password":"pw"},
          "streamSettings":{"network":"raw","security":"tls","tlsSettings":{"serverName":"example.com"}},"tag":"proxy"},
          {"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"block"},{"protocol":"dns","tag":"dns-out"}],
        "policy":{"system":{"statsOutboundUplink":true}},"routing":{"domainStrategy":"AsIs","rules":[]},"stats":{}}"""

    private fun args() = mutableMapOf<String, Any>(
        "identity" to "123:456", "generation" to 7, "egressId" to "e12", "protocol" to "TROJAN",
        "config" to config, "expiresAt" to "2026-10-28T01:00:00.000Z")

    @Test fun acceptsServerShapedProfile() {
        val p = XrayPolicy.parse(args(), now)
        assertEquals("e12", p.egressId)
        assertEquals("TROJAN", p.protocol)
    }

    @Test fun rejectsMalformedArguments() {
        val invalid = listOf<Pair<String, Any>>(
            "identity" to "123:456\n", "generation" to 0, "generation" to 1.5,
            "egressId" to "12", "egressId" to "e0", "egressId" to "e01", "egressId" to "e1 ",
            "protocol" to "WIREGUARD", "protocol" to "TUIC",
            "expiresAt" to "2026-09-28T00:59:59Z", "expiresAt" to "2028-09-28T01:00:00Z",
            "expiresAt" to "2026-09-31T01:00:00Z", "expiresAt" to "2026-10-01T01:00:00+00:00",
            "config" to "", "config" to "not json", "privateKey" to "x")
        for ((field, value) in invalid) {
            val a = args(); a[field] = value
            try { XrayPolicy.parse(a, now); fail("accepted $field=$value") } catch (_: Exception) {}
        }
    }

    @Test fun rejectsDangerousConfigShapes() {
        val bad = listOf(
            // 在设备上开出本地代理端口
            config.replace("\"port\":0,\"protocol\":\"tun\"", "\"listen\":\"0.0.0.0\",\"port\":1080,\"protocol\":\"socks\""),
            config.replace("\"inbounds\":[", "\"inbounds\":[{\"protocol\":\"socks\",\"port\":1080},"),
            // 关闭证书校验（任何位置、任何大小写）
            config.replace("\"serverName\":\"example.com\"", "\"serverName\":\"example.com\",\"AllowInsecure\":true"),
            // 写访问日志
            config.replace("\"loglevel\":\"warning\"", "\"loglevel\":\"warning\",\"access\":\"/sdcard/a.log\""),
            // 未知顶层能力（例如 api/反向代理）
            config.replace("\"stats\":{}", "\"stats\":{},\"api\":{\"tag\":\"api\"}"),
            config.replace("\"stats\":{}", "\"stats\":{},\"reverse\":{}"),
            // 未验证的出站协议、缺少 proxy 出站、链式转发绕开路由
            config.replace("\"protocol\":\"trojan\"", "\"protocol\":\"wireguard\""),
            config.replace("\"tag\":\"proxy\"", "\"tag\":\"other\""),
            config.replace("\"network\":\"raw\",", "\"network\":\"raw\",\"sockopt\":{\"dialerProxy\":\"direct\"},"),
            // 任意位置出现 listen
            config.replace("\"domainStrategy\":\"AsIs\"", "\"domainStrategy\":\"AsIs\",\"x\":{\"listen\":\"1\"}"),
        )
        for (c in bad) {
            assertNotEquals("测试用例本身没有改动配置", config, c)
            try { XrayPolicy.checkConfig(c); fail("accepted: $c") } catch (_: Exception) {}
        }
        XrayPolicy.checkConfig(config)
    }

    @Test fun trafficDeltaOnlySumsWellFormedPositiveCounters() {
        assertEquals(300L to 50L, XrayPolicy.trafficDelta("proxy,uplink,100;direct,uplink,200;proxy,downlink,50;"))
        assertEquals(0L to 0L, XrayPolicy.trafficDelta(null))
        assertEquals(0L to 0L, XrayPolicy.trafficDelta("proxy,uplink,-5;x,downlink,abc;bad;proxy,sideways,9;"))
    }
}
