import Flutter
import Network
import NetworkExtension
import WireGuardKit

final class VpnChannel:NSObject,FlutterStreamHandler {
    private var sink:FlutterEventSink?
    private var manager:NETunnelProviderManager?
    private var generation=0
    private var identity:String?
    private var timer:Timer?
    private var sampling=false
    private var sampleSequence=0
    private var connected=false
    private var lastBytes:(Int64,Int64,TimeInterval)=(0,0,0)
    private let extensionID="com.yuhai.yuhaiVpn.PacketTunnel"
    func register(_ messenger:FlutterBinaryMessenger) {
        FlutterMethodChannel(name:"yuhai/vpn",binaryMessenger:messenger).setMethodCallHandler { [weak self] call,result in self?.handle(call,result:result) }
        FlutterEventChannel(name:"yuhai/vpn/events",binaryMessenger:messenger).setStreamHandler(self)
    }
    func onListen(withArguments arguments:Any?,eventSink events:@escaping FlutterEventSink)->FlutterError? {sink=events;return nil}
    func onCancel(withArguments arguments:Any?)->FlutterError? {sink=nil;return nil}
    private func fail(_ result:FlutterResult,_ code:String) {result(FlutterError(code:code,message:"VPN operation failed",details:nil))}
    private func load(_ done:@escaping(NETunnelProviderManager?,Error?)->Void) {
        NETunnelProviderManager.loadAllFromPreferences { [weak self] managers,error in
            guard let self=self,error==nil else {done(nil,error);return}
            let manager=managers?.first {($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier==self.extensionID} ?? NETunnelProviderManager()
            if manager.protocolConfiguration==nil {let p=NETunnelProviderProtocol();p.providerBundleIdentifier=self.extensionID;p.serverAddress="宇海VPN";manager.protocolConfiguration=p;manager.localizedDescription="宇海VPN"}
            self.manager=manager;done(manager,nil)
        }
    }
    private func handle(_ call:FlutterMethodCall,result:@escaping FlutterResult) {
        let args=call.arguments as? [String:Any] ?? [:]
        switch call.method {
        case "publicKey": do {guard let id=args["identity"] as? String else {throw VpnKeyStore.Failure.invalid};result(try VpnKeyStore.key(id,create:true).publicKey.base64Key)}catch{fail(result,"ENGINE_UNAVAILABLE")}
        case "prepare": load { [weak self] manager,error in
            guard let self=self,let manager=manager,error==nil else {result(false);return}
            manager.isEnabled=true;manager.saveToPreferences { error in
                guard error==nil else {result(false);return}
                manager.loadFromPreferences {error in result(error==nil)}
            }
        }
        case "connect":
            guard VpnKeyStore.validProfile(args),let gen=args["generation"] as? Int,gen>generation,let id=args["identity"] as? String else {fail(result,"CONNECT_FAILED");return}
            generation=gen;timer?.invalidate();sampling=false;identity=id;connected=false;lastBytes=(0,0,ProcessInfo.processInfo.systemUptime)
            load { [weak self] manager,error in
                guard let self=self,gen==self.generation,let manager=manager,error==nil,let p=manager.protocolConfiguration as? NETunnelProviderProtocol else {result(FlutterError(code:"CONNECT_FAILED",message:nil,details:nil));return}
                p.providerConfiguration=args;manager.protocolConfiguration=p;manager.isEnabled=true
                manager.saveToPreferences { error in
                    guard error==nil,gen==self.generation else {self.fail(result,"CONNECT_FAILED");return}
                    manager.loadFromPreferences { error in
                        guard error==nil,gen==self.generation else {self.fail(result,"CONNECT_FAILED");return}
                        do {try manager.connection.startVPNTunnel();self.startPolling(gen,id);result(nil)}catch{self.fail(result,"CONNECT_FAILED")}
                    }
                }
            }
        case "disconnect":
            guard let gen=args["generation"] as? Int,gen>generation else {fail(result,"DISCONNECT_FAILED");return}
            generation=gen;timer?.invalidate();timer=nil;sampling=false;sampleSequence += 1
            // After a process restart the manager may not be loaded, while its
            // extension is still running. Load it before acknowledging teardown.
            load { [weak self] manager,error in
                guard let self=self,gen==self.generation,let manager=manager,error==nil else {result(FlutterError(code:"DISCONNECT_FAILED",message:nil,details:nil));return}
                manager.connection.stopVPNTunnel()
                self.waitForStop(gen,until:Date().addingTimeInterval(10),result:result)
            }
        case "probe":
            guard let host=args["host"] as? String,host.range(of:"^[A-Za-z0-9:._-]{1,255}$",options:.regularExpression) != nil,
                  let port=args["port"] as? Int,(1...65535).contains(port),let rawTimeout=args["timeoutMs"] as? Int,(1...5000).contains(rawTimeout),let endpointPort=NWEndpoint.Port(rawValue:UInt16(port)) else {result(nil);return}
            let connection=NWConnection(host:NWEndpoint.Host(host),port:endpointPort,using:.tcp)
            let start=ProcessInfo.processInfo.systemUptime;var finished=false
            func complete(_ value:Int?) {if finished{return};finished=true;connection.cancel();result(value)}
            connection.stateUpdateHandler={state in switch state {case .ready:complete(Int((ProcessInfo.processInfo.systemUptime-start)*1000));case .failed,.cancelled:complete(nil);default:break}}
            connection.start(queue:.main);DispatchQueue.main.asyncAfter(deadline:.now()+Double(rawTimeout)/1000){complete(nil)}
        default:result(FlutterMethodNotImplemented)
        }
    }
    private func waitForStop(_ gen:Int,until:Date,result:@escaping FlutterResult) {
        guard gen==generation else {fail(result,"DISCONNECT_FAILED");return}
        let state=manager?.connection.status ?? .disconnected
        if state == .disconnected || state == .invalid {identity=nil;result(nil);return}
        if Date()>=until {fail(result,"DISCONNECT_FAILED");return}
        DispatchQueue.main.asyncAfter(deadline:.now()+0.1){[weak self] in self?.waitForStop(gen,until:until,result:result)}
    }
    private func startPolling(_ gen:Int,_ id:String) {
        timer?.invalidate();timer=Timer.scheduledTimer(withTimeInterval:1,repeats:true){[weak self] _ in self?.sample(gen,id)}
        sink?(["type":"status","status":"connecting","generation":gen,"identity":id])
    }
    private func sample(_ gen:Int,_ id:String) {
        guard gen==generation,identity==id,!sampling,let session=manager?.connection as? NETunnelProviderSession else{return}
        if session.status == .disconnected || session.status == .invalid {timer?.invalidate();sink?(["type":"status","status":"disconnected","generation":gen,"identity":id]);return}
        guard session.status == .connected else{return}
        sampling=true;sampleSequence += 1
        let sampleID=sampleSequence
        DispatchQueue.main.asyncAfter(deadline:.now()+5) { [weak self] in
            guard let self=self,gen==self.generation,sampleID==self.sampleSequence,self.sampling else {return}
            self.sampling=false;self.timer?.invalidate();session.stopTunnel()
            self.sink?(["type":"error","code":"NETWORK_UNAVAILABLE","generation":gen,"identity":id])
        }
        do {try session.sendProviderMessage(Data("status".utf8)){[weak self] raw in DispatchQueue.main.async {
            guard let self=self,gen==self.generation,sampleID==self.sampleSequence,self.sampling else{return};self.sampling=false
            guard gen==self.generation,id==self.identity,let raw=raw,let data=(try? JSONSerialization.jsonObject(with:raw)) as? [String:Any],data["identity"] as? String==id,data["generation"] as? Int==gen else{return}
            guard data["handshakeConfirmed"] as? Bool==true else {
                if self.connected {self.connected=false;self.sink?(["type":"status","status":"reconnecting","generation":gen,"identity":id])}
                return
            }
            if !self.connected {self.connected=true;self.sink?(data.merging(["type":"status","status":"connected"],uniquingKeysWith:{$1}))}
            let up=(data["upBytes"] as? NSNumber)?.int64Value ?? 0,down=(data["downBytes"] as? NSNumber)?.int64Value ?? 0
            let now=ProcessInfo.processInfo.systemUptime,delta=max(0.001,now-self.lastBytes.2)
            self.sink?(data.merging(["type":"stats","upSpeed":Int64(Double(max(0,up-self.lastBytes.0))/delta),"downSpeed":Int64(Double(max(0,down-self.lastBytes.1))/delta)],uniquingKeysWith:{$1}))
            self.lastBytes=(up,down,now)
        }}}catch{sampling=false;timer?.invalidate();session.stopTunnel();sink?(["type":"error","code":"NETWORK_UNAVAILABLE","generation":gen,"identity":id])}
    }
    deinit {timer?.invalidate()}
}
