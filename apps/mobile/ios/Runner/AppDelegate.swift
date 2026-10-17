import Flutter
import UIKit

@main
@objc class AppDelegate: FlutterAppDelegate, FlutterImplicitEngineDelegate {
  override func application(_ application: UIApplication,
                            didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
    return super.application(application, didFinishLaunchingWithOptions: launchOptions)
  }

  private let vpnChannel = VpnChannel()

  func didInitializeImplicitFlutterEngine(_ engineBridge: FlutterImplicitEngineBridge) {
    GeneratedPluginRegistrant.register(with: engineBridge.pluginRegistry)
    vpnChannel.register(engineBridge.applicationRegistrar.messenger())
    let deviceKey = DeviceKeyChannel()
    FlutterMethodChannel(name: DeviceKeyChannel.name, binaryMessenger: engineBridge.applicationRegistrar.messenger())
      .setMethodCallHandler { call, result in deviceKey.handle(call, result: result) }
  }
}
