// Copyright 2023 Vic Shóstak and Create Go App Contributors. All rights reserved.
// Use of this source code is governed by Apache 2.0 license
// that can be found in the LICENSE file.

package solver

import (
	"strings"

	"golang.org/x/mod/semver"
)

// satisfiesVersion compares an offered version against a requirement.
//
// It supports the common operators >=, <=, >, <, =, ^ and ~ on semver-ish
// versions (a leading "v" is added when missing). Non-semantic versions such
// as "latest" always match: the catalog uses attributes, not versions, for
// concrete compatibility, and blocking on marketing tags would be wrong.
func satisfiesVersion(got, constraint string) bool {
	if constraint == "" {
		return true
	}
	gotV := canonicalize(got)
	want := strings.TrimSpace(constraint)
	if want == "" {
		return true
	}

	op := "="
	for _, prefix := range []string{">=", "<=", "^", "~", ">", "<", "="} {
		if strings.HasPrefix(want, prefix) {
			op = prefix
			want = strings.TrimSpace(strings.TrimPrefix(want, prefix))
			break
		}
	}
	wantV := canonicalize(want)
	if !semver.IsValid(gotV) || !semver.IsValid(wantV) {
		return true
	}

	cmp := semver.Compare(gotV, wantV)
	switch op {
	case ">=":
		return cmp >= 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	case "~":
		return cmp >= 0 && sameMinor(gotV, wantV)
	case "^":
		return cmp >= 0 && sameMajor(gotV, wantV)
	default:
		return cmp == 0
	}
}

func canonicalize(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

func sameMajor(a, b string) bool {
	return semver.Major(a) == semver.Major(b)
}

func sameMinor(a, b string) bool {
	return semver.MajorMinor(a) == semver.MajorMinor(b)
}
