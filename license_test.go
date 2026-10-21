// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package wb2api

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const repoURL = "https://github.com/yreidev/workbuddy2api-panel"

// licenseHeader 源文件开头的许可声明（AGPL-3.0 第 7 条要求在源文件里写明附加条款去哪里看），
// 按注释写法分三种；Go 文件头后面必须空一行，免得并进包注释或构建约束。
var licenseHeader = []string{
	"SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）",
	"SPDX-License-Identifier: AGPL-3.0-only",
	"复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：" + repoURL,
}

// headerStyle 按文件名给出注释前缀；不需要文件头的返回空串（配置、文档、测试数据、前端产物）。
func headerStyle(path string) string {
	switch {
	case strings.Contains(path, "/testdata/"), strings.HasPrefix(path, "internal/panel/dist/"):
		return ""
	case strings.HasSuffix(path, ".go"), strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".tsx"), strings.HasSuffix(path, ".js"):
		return "// "
	case strings.HasSuffix(path, ".css"):
		return " * "
	case strings.HasSuffix(path, ".sh"), strings.HasSuffix(path, ".py"), path == ".githooks/commit-msg":
		return "# "
	}
	return ""
}

// 每个源文件开头都要有许可声明：新建的文件照着别的文件抄三行（脚本放在 #! 那行之后）。
func TestSourceFilesHaveLicenseHeader(t *testing.T) {
	out, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("不在 git 仓库里，跳过：%v", err)
	}
	var missing []string
	checked := 0
	for _, path := range strings.Split(string(out), "\x00") {
		prefix := headerStyle(path)
		if path == "" || prefix == "" {
			continue
		}
		data, err := os.ReadFile(filepath.FromSlash(path))
		if err != nil {
			t.Fatalf("读 %s：%v", path, err)
		}
		checked++
		lines := strings.SplitN(string(data), "\n", 9)
		head := strings.Join(lines[:min(8, len(lines))], "\n")
		found := true
		for _, line := range licenseHeader {
			if !strings.Contains(head, prefix+line) {
				found = false
				break
			}
		}
		switch {
		case !found:
			missing = append(missing, path)
		case strings.HasSuffix(path, ".go") && !bytes.HasPrefix(data, []byte("// "+licenseHeader[0]+"\n// "+licenseHeader[1]+"\n// "+licenseHeader[2]+"\n\n")):
			missing = append(missing, path+"（Go 文件要从第一行开始写，后面空一行）")
		}
	}
	if checked < 300 {
		t.Fatalf("只查到 %d 个源文件，筛选规则可能写错了", checked)
	}
	if len(missing) > 0 {
		t.Errorf("这些文件开头缺少许可声明，照下面三行补上（注释符号按文件类型换）：\n  // %s\n\n%s",
			strings.Join(licenseHeader, "\n  // "), strings.Join(missing, "\n"))
	}
}

// LICENSE 只放 AGPL-3.0 原文，GitHub 才认得出许可；附加条款和上游的 MIT 声明在 NOTICE。
func TestLicenseAndNotice(t *testing.T) {
	license, err := os.ReadFile("LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(license)), "GNU AFFERO GENERAL PUBLIC LICENSE\n                       Version 3, 19 November 2007") {
		t.Fatal("LICENSE 不是 AGPL-3.0 原文")
	}
	notice, err := os.ReadFile("NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		repoURL,
		"AGPL-3.0-only",
		"Copyright (c) 2026 Sliverkiss (original project: https://github.com/Sliverkiss/workbuddy2api)",
		"Copyright (c) 2026 linguo2625469 (fork: https://github.com/linguo2625469/workbuddy2api-panel)",
		"Permission is hereby granted, free of charge",
	} {
		if !strings.Contains(string(notice), want) {
			t.Errorf("NOTICE 里缺少 %q", want)
		}
	}
}
