package com.yuhai.yuhai_vpn

import java.net.Inet6Address
import java.net.InetAddress
import java.time.Instant
import java.util.Base64

/** Strict public-material validation before any key access or tunnel mutation. */
internal object WireGuardPolicy {
    data class Profile(val identity: String, val expires: Long, val address: String,
        val endpoint: String, val publicKey: String, val dns: List<String>, val mtu: Int)
    private val fields = setOf("identity", "generation", "nodeId", "protocol", "endpoint",
        "serverPublicKey", "address", "dns", "mtu", "expiresAt")
    fun identity(id: String) = id.matches(Regex("[A-Za-z0-9_-]{1,64}:[A-Za-z0-9_-]{1,64}"))
    fun integer(value: Any?): Long? = when (value) { is Int -> value.toLong(); is Long -> value; else -> null }
    private fun ipv4(s: String): List<Int>? {
        val parts = s.split('.')
        if (parts.size != 4 || parts.any { !it.matches(Regex("0|[1-9][0-9]{0,2}")) }) return null
        val values = parts.map(String::toInt)
        return values.takeIf { it.all { n -> n <= 255 } }
    }
    private fun ip(s: String): Boolean {
        ipv4(s)?.let { return it[0] != 0 && it[0] < 224 }
        if (!s.contains(':') || !s.matches(Regex("[0-9a-fA-F:]{2,39}"))) return false
        return runCatching { InetAddress.getByName(s) }.getOrNull()?.let {
            it is Inet6Address && !it.isAnyLocalAddress && !it.isMulticastAddress
        } ?: false
    }
    fun host(s: String): Boolean {
        if (ip(s)) return true
        return s.length <= 253 && s.contains('.') && !s.matches(Regex("[0-9.]+")) &&
            s.split('.').all { it.matches(Regex("[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?")) }
    }
    fun endpoint(s: String): Boolean {
        if (s.length > 320) return false
        val match = Regex("(\\[[0-9A-Fa-f:]+\\]|[^:]+):([1-9][0-9]{0,4})").matchEntire(s) ?: return false
        val rawHost = match.groupValues[1]
        val h = rawHost.removePrefix("[").removeSuffix("]")
        return host(h) && (!rawHost.startsWith("[") || h.contains(':')) && match.groupValues[2].toInt() <= 65535
    }
    fun parse(args: Map<*, *>, now: Long): Profile {
        require(args.keys == fields)
        val id = args["identity"] as? String ?: error("identity")
        require(identity(id) && args["protocol"] == "WIREGUARD")
        require((integer(args["generation"]) ?: 0) > 0)
        val node = args["nodeId"] as? String ?: error("node")
        require(node.toLongOrNull()?.let { it > 0 && it.toString() == node } == true)
        val rawExpiry = args["expiresAt"] as? String ?: error("expiry")
        require(rawExpiry.matches(Regex("[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\\.[0-9]+)?Z")))
        val instant = Instant.parse(rawExpiry)
        require(instant.toString().take(19) == rawExpiry.take(19))
        val expiry = instant.toEpochMilli()
        require(expiry > now && expiry - now <= 305000)
        val address = args["address"] as? String ?: error("address")
        require(address.endsWith("/32"))
        val v = ipv4(address.removeSuffix("/32")) ?: error("address")
        require(v[0] == 10 || (v[0] == 172 && v[1] in 16..31) || (v[0] == 192 && v[1] == 168))
        val endpoint = args["endpoint"] as? String ?: error("endpoint")
        require(endpoint(endpoint))
        val publicKey = args["serverPublicKey"] as? String ?: error("key")
        val decoded = Base64.getDecoder().decode(publicKey)
        require(decoded.size == 32 && decoded.any { it != 0.toByte() } && Base64.getEncoder().encodeToString(decoded) == publicKey)
        val dns = (args["dns"] as? List<*>)?.map { it as? String ?: error("dns") } ?: error("dns")
        require(dns.size in 1..8 && dns.distinct().size == dns.size && dns.all(::ip))
        // Both IP families are routed: IPv6 requires an MTU of at least 1280.
        val mtu = integer(args["mtu"]) ?: error("mtu")
        require(mtu in 1280..1500)
        return Profile(id, expiry, address, endpoint, publicKey, dns, mtu.toInt())
    }
}
