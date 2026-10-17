# invite

- data/：接口调用与本地缓存（只依赖 core/network）
- domain/：实体与用例
- presentation/：页面与 Riverpod 状态；页面只依赖用例，不直接调用接口
