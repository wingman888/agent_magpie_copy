# agent_magpie_copy

RSI 工作台（[LtCodePipeline](https://github.com/wingman888/LtCodePipeline)）对标 [yetone/magpie](https://github.com/yetone/magpie) 的检查仓库。

- `bench/**` 分支由评测工具自动推送：每个分支是一次检查，内容为 magpie 某个提交上的 RSI 实现（MIT 许可），推送时附带工作流在 Actions 上运行 `go vet` / `go test`，结果经 check-runs 读取后分支即被删除。
- `main` 只保存本说明。
