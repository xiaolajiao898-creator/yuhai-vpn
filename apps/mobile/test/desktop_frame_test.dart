import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:yuhai_vpn/app/desktop_frame.dart';

/// 电脑窗口拉宽、最大化时整个界面排在居中的一栏里；默认窗口、手机不变。
void main() {
  Future<void> pumpAt(WidgetTester tester, Size size, Widget app) async {
    tester.view.physicalSize = size;
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await tester.pumpWidget(app);
  }

  Widget app({bool enabled = true, Widget? home}) => MaterialApp(
        builder: (context, child) => DesktopFrame(
          enabled: enabled,
          child: child ?? const SizedBox.shrink(),
        ),
        home: home ?? const Scaffold(body: SizedBox.expand(key: Key('page'))),
      );

  testWidgets('最大化的窗口里，页面在居中、最宽 480 的一栏里', (tester) async {
    await pumpAt(tester, const Size(1600, 900), app());
    final rect = tester.getRect(find.byKey(const Key('page')));
    expect(rect.width, DesktopFrame.defaultMaxWidth);
    expect(rect.left, (1600 - DesktopFrame.defaultMaxWidth) / 2);
    expect(rect.top, 0);
    expect(rect.height, 900);
  });

  testWidgets('栏里的页面看到的 MediaQuery 宽度是栏宽', (tester) async {
    Size? seen;
    await pumpAt(
      tester,
      const Size(1600, 900),
      app(home: Builder(builder: (context) {
        seen = MediaQuery.sizeOf(context);
        return const SizedBox.shrink();
      })),
    );
    expect(seen, const Size(480, 900));
  });

  testWidgets('默认大小的电脑窗口不变', (tester) async {
    await pumpAt(tester, const Size(424, 820), app());
    expect(tester.getRect(find.byKey(const Key('page'))),
        const Rect.fromLTWH(0, 0, 424, 820));
  });

  testWidgets('手机上不启用，宽屏也铺满', (tester) async {
    await pumpAt(tester, const Size(1600, 900), app(enabled: false));
    expect(tester.getRect(find.byKey(const Key('page'))).width, 1600);
  });

  testWidgets('把窗口从默认大小拉到最大，页面状态不丢', (tester) async {
    await pumpAt(tester, const Size(424, 820), app(home: const _Counter()));
    await tester.tap(find.byKey(const Key('add')));
    await tester.pump();
    expect(find.text('1'), findsOneWidget);

    tester.view.physicalSize = const Size(1600, 900);
    await tester.pump();
    expect(find.text('1'), findsOneWidget);
    expect(tester.getRect(find.byType(Scaffold)).width,
        DesktopFrame.defaultMaxWidth);
  });
}

class _Counter extends StatefulWidget {
  const _Counter();

  @override
  State<_Counter> createState() => _CounterState();
}

class _CounterState extends State<_Counter> {
  var _n = 0;

  @override
  Widget build(BuildContext context) => Scaffold(
        body: Center(child: Text('$_n')),
        floatingActionButton: FloatingActionButton(
          key: const Key('add'),
          onPressed: () => setState(() => _n++),
          child: const Icon(Icons.add),
        ),
      );
}
