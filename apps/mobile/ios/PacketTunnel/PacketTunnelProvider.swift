import Foundation
import NetworkExtension
import WireGuardKit

final class PacketTunnelProvider:NEPacketTunnelProvider {
    private lazy var adapter=WireGuardAdapter(with:self) { _,_ in } // never log key-bearing runtime text
    private let state = DispatchQueue(label:"com.yuhai.vpn.provider-state")
    private var operation = 0
    private var active = false
    private var identity=""
    private var generation=0
    private var peerHex=""
    private var started=Date()
    private var connected=false
    override func startTunnel(options:[String:NSObject]?,completionHandler:@escaping(Error?)->Void) {
        state.async { self.startValidated(completionHandler) }
    }
    private func startValidated(_ completionHandler:@escaping(Error?)->Void) {
        operation += 1
        let requested = operation
        active = false
        do {
            guard let proto=protocolConfiguration as? NETunnelProviderProtocol,
                  let p=proto.providerConfiguration, VpnKeyStore.validProfile(p),
                  let id=p["identity"] as? String,let gen=p["generation"] as? Int,
                  let rawExpiry=p["expiresAt"] as? String,let expiry=Self.date(rawExpiry),expiry>Date(),
                  p["protocol"] as? String=="WIREGUARD",
                  let keyText=p["serverPublicKey"] as? String,let publicKey=PublicKey(base64Key:keyText),
                  let addressText=p["address"] as? String,let address=IPAddressRange(from:addressText),
                  addressText.hasSuffix("/32"),
                  let endpointText=p["endpoint"] as? String,let endpoint=Endpoint(from:endpointText),
                  let dns=p["dns"] as? [String],!dns.isEmpty,dns.count<=8,
                  let mtu=p["mtu"] as? Int,(1280...1500).contains(mtu) else { throw VpnKeyStore.Failure.invalid }
            let key=try VpnKeyStore.key(id)
            guard key.publicKey != publicKey else {throw VpnKeyStore.Failure.invalid}
            var iface=InterfaceConfiguration(privateKey:key);iface.addresses=[address];iface.mtu=UInt16(mtu)
            iface.dns=try dns.map { guard let result=DNSServer(from:$0) else { throw VpnKeyStore.Failure.invalid };return result }
            var peer=PeerConfiguration(publicKey:publicKey);peer.endpoint=endpoint
            peer.allowedIPs=[IPAddressRange(from:"0.0.0.0/0")!,IPAddressRange(from:"::/0")!];peer.persistentKeepAlive=25
            identity=id;generation=gen;peerHex=publicKey.hexKey;started=Date();connected=false
            adapter.start(tunnelConfiguration:TunnelConfiguration(name:"宇海",interface:iface,peers:[peer])) { error in
                self.state.async {
                    guard requested == self.operation else {completionHandler(VpnKeyStore.Failure.unavailable);return}
                    self.active = error == nil
                    completionHandler(error == nil ? nil : VpnKeyStore.Failure.unavailable)
                    if self.active {self.checkInitialHandshake(requested)}
                }
            }
        } catch { completionHandler(VpnKeyStore.Failure.invalid) }
    }
    override func stopTunnel(with reason:NEProviderStopReason,completionHandler:@escaping()->Void) {
        state.async {
            self.operation += 1;self.active=false;self.connected=false
            self.adapter.stop { _ in completionHandler() }
        }
    }
    private func checkInitialHandshake(_ requested:Int) {
        state.asyncAfter(deadline:.now()+30) {
            guard requested == self.operation,self.active,!self.connected else {return}
            // Never leave an unconfirmed tunnel running when the app disappears.
            self.handleAppMessage(Data("status".utf8)) { _ in
                self.state.async {
                    guard requested == self.operation,self.active,!self.connected else {return}
                    self.active=false;self.operation += 1
                    self.adapter.stop { _ in self.cancelTunnelWithError(VpnKeyStore.Failure.unavailable) }
                }
            }
        }
    }
    override func handleAppMessage(_ messageData:Data,completionHandler:((Data?)->Void)?) {
        guard messageData==Data("status".utf8) else { completionHandler?(nil);return }
        state.async {
        guard self.active else {completionHandler?(nil);return}
        let requested = self.operation
        self.adapter.getRuntimeConfiguration { [weak self] raw in
            guard let self=self else {completionHandler?(nil);return}
            self.state.async {
            guard self.active,requested == self.operation,let raw=raw else { completionHandler?(nil);return }
            var current="",seconds:Int64=0,nanos:Int64=0,rx:Int64=0,tx:Int64=0
            for line in raw.split(separator:"\n") {
                let pair=line.split(separator:"=",maxSplits:1,omittingEmptySubsequences:false)
                guard pair.count==2 else {continue}
                if pair[0]=="public_key" { current=String(pair[1]);continue }
                guard current==self.peerHex else {continue}
                switch pair[0] { case "last_handshake_time_sec":seconds=Int64(pair[1]) ?? 0
                case "last_handshake_time_nsec":nanos=Int64(pair[1]) ?? 0
                case "rx_bytes":rx=Int64(pair[1]) ?? 0
                case "tx_bytes":tx=Int64(pair[1]) ?? 0
                default:break }
            }
            let handshake=Date(timeIntervalSince1970:Double(seconds)+Double(nanos)/1e9)
            let fresh=handshake>=self.started && handshake<=Date().addingTimeInterval(5) && Date().timeIntervalSince(handshake)<180
            self.connected=fresh
            let result:[String:Any]=["identity":self.identity,"generation":self.generation,
                "handshakeConfirmed":fresh,"handshakeAt":ISO8601DateFormatter().string(from:handshake),
                "upBytes":max(0,tx),"downBytes":max(0,rx),"since":Int64(self.started.timeIntervalSince1970*1000)]
            // Only the aggregate projection crosses to the app; no runtime keys/config.
            completionHandler?(try? JSONSerialization.data(withJSONObject:result))
            }
        }
        }
    }
    static func date(_ raw:String)->Date? {
        let f=ISO8601DateFormatter();f.formatOptions=[.withInternetDateTime,.withFractionalSeconds]
        return f.date(from:raw) ?? ISO8601DateFormatter().date(from:raw)
    }
}
