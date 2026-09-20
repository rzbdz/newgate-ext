module github.com/rzbdz/newgate-modules-ext/go

go 1.27

require github.com/rzbdz/newgate/go v0.0.0

// 内核是**这个仓库里的一份普通依赖**，钉在 core/ 这个 submodule 的提交上。
//
// 为什么是 replace 而不是让 Go 去下载一个版本号：submodule 的 gitlink 已经是
// 「用哪个内核」的唯一真相，再写一个版本号就是同一件事记两遍——两处迟早对不上，
// 而对不上的症状是「本地编出来的和 CI 编出来的不是一个东西」。
//
// 另外两个好处正好是这一版要的东西：
//   - 内核的测试可以在树内直接跑（发行版流水线里的 core-test 那一步就是这么来的）；
//   - 构建读的是**工作区**，所以「改完先 commit 才能编」这条摩擦没有了。
//
// 代价写在文档里（docs/09-extension-guide.md §8）：本地构建与 CI 构建的差别只剩
// 「submodule 在不在那一发上」，而那是 git 自己保证的事。
replace github.com/rzbdz/newgate/go => ../core/go
