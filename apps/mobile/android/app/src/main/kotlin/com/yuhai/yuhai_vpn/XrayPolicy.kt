package com.yuhai.yuhai_vpn

import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant

/**
 * 出口（Xray 内核）连接材料的原生侧校验：服务端是唯一的配置生成方，这里只做纵深防御——
 * 形状不对就不启动内核，绝不「修正」后再用。
 *
 * - 只允许一个 tun 入站、不得有任何监听地址：设备上不能因为一份配置开出本地代理端口；
 * - 出站协议限定在已验证的集合内；
 * - 任何位置出现 allowInsecure 一律拒绝（TLS 校验不能关闭，自签证书只能按指纹钉住）；
 * - 日志只允许设置级别，不允许写访问日志文件（访问日志会记下访问的站点）。
 */
internal object XrayPolicy {
    data class Profile(val identity: String, val egressId: String, val protocol: String,
        val config: String, val expires: Long)

    private val fields = setOf("identity", "generation", "egressId", "protocol", "config", "expiresAt")
    val protocols = setOf("SOCKS5", "HTTP", "HTTPS", "SHADOWSOCKS", "TROJAN", "VLESS", "VMESS", "HYSTERIA2")
    private val topKeys = setOf("log", "dns", "inbounds", "outbounds", "routing", "stats", "policy")
    private val outboundProtocols = setOf("socks", "http", "shadowsocks", "trojan", "vless", "vmess",
        "hysteria", "freedom", "blackhole", "dns")
    private const val MAX_CONFIG = 256 * 1024
    private const val MAX_VALIDITY = 400L * 24 * 3600 * 1000

    fun egressId(s: String) = s.matches(Regex("e[1-9][0-9]{0,18}"))

    fun parse(args: Map<*, *>, now: Long): Profile {
        require(args.keys == fields)
        val id = args["identity"] as? String ?: error("identity")
        require(WireGuardPolicy.identity(id))
        require((WireGuardPolicy.integer(args["generation"]) ?: 0) > 0)
        val egress = args["egressId"] as? String ?: error("egress")
        require(egressId(egress))
        val protocol = args["protocol"] as? String ?: error("protocol")
        require(protocol in protocols)
        val rawExpiry = args["expiresAt"] as? String ?: error("expiry")
        require(rawExpiry.matches(Regex("[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\\.[0-9]+)?Z")))
        val instant = Instant.parse(rawExpiry)
        require(instant.toString().take(19) == rawExpiry.take(19))
        val expiry = instant.toEpochMilli()
        require(expiry > now && expiry - now <= MAX_VALIDITY)
        val config = args["config"] as? String ?: error("config")
        checkConfig(config)
        return Profile(id, egress, protocol, config, expiry)
    }

    fun checkConfig(config: String) {
        require(config.length in 2..MAX_CONFIG)
        require(!config.contains("allowinsecure", ignoreCase = true))
        val root = JSONObject(config)
        root.keys().forEach { require(it in topKeys) }
        root.optJSONObject("log")?.let { log -> log.keys().forEach { require(it == "loglevel") } }
        val inbounds = root.getJSONArray("inbounds")
        require(inbounds.length() == 1)
        val tun = inbounds.getJSONObject(0)
        require(tun.getString("protocol") == "tun" && !tun.has("listen"))
        val outbounds = root.getJSONArray("outbounds")
        require(outbounds.length() in 1..8)
        var proxy = false
        for (i in 0 until outbounds.length()) {
            val o = outbounds.getJSONObject(i)
            require(o.getString("protocol") in outboundProtocols)
            // 出站不得再套一层本地转发（dialerProxy 指向别的出站可绕过路由规则）。
            o.optJSONObject("streamSettings")?.optJSONObject("sockopt")?.let { require(!it.has("dialerProxy")) }
            if (o.optString("tag") == "proxy") proxy = true
        }
        require(proxy)
        require(noKey(root, "listen", 0))
    }

    private fun noKey(v: Any?, key: String, depth: Int): Boolean {
        require(depth < 16)
        return when (v) {
            is JSONObject -> v.keys().asSequence().all { it != key && noKey(v.opt(it), key, depth + 1) }
            is JSONArray -> (0 until v.length()).all { noKey(v.opt(it), key, depth + 1) }
            else -> true
        }
    }

    /** libv2ray 的计数查询返回「tag,方向,字节;」并在读取后清零；这里只累加非负整数。 */
    fun trafficDelta(raw: String?): Pair<Long, Long> {
        var up = 0L
        var down = 0L
        raw?.split(';')?.forEach { item ->
            val p = item.split(',')
            if (p.size != 3) return@forEach
            val n = p[2].toLongOrNull()?.takeIf { it > 0 } ?: return@forEach
            when (p[1]) { "uplink" -> up += n; "downlink" -> down += n }
        }
        return up to down
    }
}
