#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
# SPDX-License-Identifier: AGPL-3.0-only
# 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

# 提交信息检查，规则见 docs/commits.md。
#   scripts/commit-msg.sh <消息文件>             检查一条提交信息（.githooks/commit-msg 调用）
#   scripts/commit-msg.sh --commits <rev-list 参数>  沿第一父提交检查这些提交（CI 调用），
#                                                如 --commits A..B；合并进来的上游提交不查
set -euo pipefail

# 标题按字计长，要 UTF-8 locale；macOS 没有 C.UTF-8，退到 en_US.UTF-8
for loc in C.UTF-8 en_US.UTF-8; do
	export LC_ALL=$loc
	probe=字
	[[ ${#probe} -eq 1 ]] 2>/dev/null && break
done
probe=字
if [[ ${#probe} -ne 1 ]]; then
	echo "commit-msg.sh：找不到 UTF-8 locale，没法按字计算标题长度" >&2
	exit 2
fi

types="feat fix refactor perf test docs build ci chore revert"
scopes="chat models auth login credit tasks pool session net panel server config docker release upstream repo"
max_title=50
local_mode=1

# check <提交信息> <名称>：合格返回 0；不合格把原因写到标准错误，返回 1
check() {
	local msg=$1 name=$2 head second type scope desc
	head=$(printf '%s\n' "$msg" | sed -n 1p)
	second=$(printf '%s\n' "$msg" | sed -n 2p)
	local errs=()

	# 本地 rebase 的 fixup!/squash!/amend! 临时提交放行，推上去之前要压掉
	if [[ $local_mode -eq 1 && $head =~ ^(fixup|squash|amend)!\  ]]; then
		return 0
	fi

	if [[ $head =~ ^([a-z]+)\(([a-z]+)\):\ (.*)$ ]]; then
		type=${BASH_REMATCH[1]}
		scope=${BASH_REMATCH[2]}
		desc=${BASH_REMATCH[3]}
		[[ " $types " == *" $type "* ]] || errs+=("type「$type」不在列表里：$types")
		[[ " $scopes " == *" $scope "* ]] || errs+=("scope「$scope」不在列表里：$scopes")
		if [[ -z ${desc// /} ]]; then
			errs+=("标题是空的")
		else
			[[ $desc == " "* || $desc == *" " ]] && errs+=("标题前后不要留空格")
			[[ $desc == *"。" || $desc == *"." ]] && errs+=("标题末尾不加句号")
			((${#desc} <= max_title)) || errs+=("标题 ${#desc} 字，超过 $max_title 字")
		fi
		if [[ $type/$scope == chore/release ]]; then
			[[ $desc =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || errs+=("发版提交的标题只写版本号，如 chore(release): 1.2.3")
		elif [[ $desc =~ [0-9]+\.[0-9]+\.[0-9]+ ]]; then
			errs+=("版本号只出现在 chore(release) 的标题里，其他提交写进正文")
		fi
		if [[ $scope == upstream ]]; then
			[[ $type == chore && $desc =~ ^同步上游\ [0-9a-f]{7,40}$ ]] || errs+=("同步上游写成 chore(upstream): 同步上游 <上游短 SHA>")
		fi
	else
		errs+=("标题格式应为 <type>(<scope>): <标题>，如 fix(chat): 上游 11128 不再判成封号")
	fi

	[[ -z $second ]] || errs+=("标题和正文之间要空一行")
	if printf '%s\n' "$msg" | grep -qi '^co-authored-by:'; then
		errs+=("不加 Co-authored-by 署名")
	fi

	((${#errs[@]} == 0)) && return 0
	{
		echo "提交信息不合格（$name）：$head"
		printf '  - %s\n' "${errs[@]}"
	} >&2
	return 1
}

if [[ ${1:-} == --commits ]]; then
	shift
	local_mode=0
	failed=0
	n=0
	while read -r sha; do
		n=$((n + 1))
		check "$(git log -1 --format=%B "$sha")" "${sha:0:7}" || failed=1
	done < <(git rev-list --first-parent "$@")
	if [[ $failed -ne 0 ]]; then
		echo "规则见 docs/commits.md；改已推送的提交要先 rebase 改好再推" >&2
		exit 1
	fi
	echo "检查了 $n 个提交，提交信息都合格"
	exit 0
fi

if [[ $# -ne 1 || ! -f $1 ]]; then
	echo "用法：scripts/commit-msg.sh <消息文件> | --commits <rev-list 参数>" >&2
	exit 2
fi

# 钩子拿到的是编辑器里的原文：去掉剪刀线以下（commit -v 的 diff）和 # 注释行，再去掉开头的空行
msg=$(sed -e '/^# -\{24\} >8 -\{24\}$/,$d' -e '/^#/d' "$1" | sed -e '/./,$!d')
if [[ -z ${msg//[[:space:]]/} ]]; then
	exit 0 # 空消息由 git 自己中止提交
fi
if ! check "$msg" "本次提交"; then
	echo "规则见 docs/commits.md；原消息还在 .git/COMMIT_EDITMSG，可以 git commit -e -F .git/COMMIT_EDITMSG 接着改" >&2
	exit 1
fi
