import Cocoa
import FlutterMacOS

class MainFlutterWindow: NSWindow {
  override func awakeFromNib() {
    let flutterViewController = FlutterViewController()
    // YUHAI_WINDOW：默认窗口大小
    var windowFrame = self.frame
    windowFrame.size = NSSize(width: 440, height: 860)
    self.minSize = NSSize(width: 400, height: 720)
    self.title = "宇海"
    self.contentViewController = flutterViewController
    self.setFrame(windowFrame, display: true)

    RegisterGeneratedPlugins(registry: flutterViewController)

    super.awakeFromNib()
  }
}
