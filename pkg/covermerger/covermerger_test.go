// Copyright 2024 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package covermerger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// nolint: lll
func TestAggregateStreamData(t *testing.T) {
	testsPath := "testdata/integration"
	type Test struct {
		name              string
		workdir           string
		bqTable           string
		simpleAggregation string
		baseRepo          string
		baseCommit        string
	}
	tests := []Test{
		{
			name:              "aesni-intel_glue",
			workdir:           testsPath + "/aesni-intel_glue/test-workdir-covermerger",
			bqTable:           readFileOrFail(t, testsPath+"/aesni-intel_glue/bqTable.txt"),
			simpleAggregation: readFileOrFail(t, testsPath+"/aesni-intel_glue/merge_result.txt"),
			baseRepo:          "git://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git",
			baseCommit:        "fe46a7dd189e25604716c03576d05ac8a5209743",
		},
		{
			name:    "code deleted",
			workdir: testsPath + "/all/test-workdir-covermerger",
			bqTable: `timestamp,version,fuzzing_minutes,arch,build_id,manager,kernel_repo,kernel_branch,kernel_commit,file_path,func_name,sl,sc,el,ec,hit_count,inline,pc
samp_time,1,360,arch,b1,ci-mock,git://repo,master,commit1,delete_code.c,func1,2,0,2,-1,1,true,1`,
			simpleAggregation: `{
  "delete_code.c":
  {
    "HitCounts":{},
		"FileExists": true
  }
}`,
			baseRepo:   "git://repo",
			baseCommit: "commit2",
		},
		{
			name:    "file deleted",
			workdir: testsPath + "/all/test-workdir-covermerger",
			bqTable: `timestamp,version,fuzzing_minutes,arch,build_id,manager,kernel_repo,kernel_branch,kernel_commit,file_path,func_name,sl,sc,el,ec,hit_count,inline,pc
samp_time,1,360,arch,b1,ci-mock,git://repo,master,commit1,delete_file.c,func1,2,0,2,-1,1,true,1`,
			simpleAggregation: `{
  "delete_file.c":
  {
		"FileExists": false
  }
}`,
			baseRepo:   "git://repo",
			baseCommit: "commit2",
		},
		{
			name:    "covered line changed",
			workdir: testsPath + "/all/test-workdir-covermerger",
			bqTable: `timestamp,version,fuzzing_minutes,arch,build_id,manager,kernel_repo,kernel_branch,kernel_commit,file_path,func_name,sl,sc,el,ec,hit_count,inline,pc
samp_time,1,360,arch,b1,ci-mock,git://repo,master,commit1,change_line.c,func1,2,0,2,-1,1,true,1
samp_time,1,360,arch,b1,ci-mock,git://repo,master,commit1,change_line.c,func1,3,0,3,-1,1,true,1`,
			simpleAggregation: `{
  "change_line.c":
  {
		"HitCounts":{"3": 1},
		"FileExists": true
  }
}`,
			baseRepo:   "git://repo",
			baseCommit: "commit2",
		},
		{
			name:    "add line",
			workdir: testsPath + "/all/test-workdir-covermerger",
			bqTable: `timestamp,version,fuzzing_minutes,arch,build_id,manager,kernel_repo,kernel_branch,kernel_commit,file_path,func_name,sl,sc,el,ec,hit_count,inline,pc
samp_time,1,360,arch,b1,ci-mock,git://repo,master,commit1,add_line.c,func1,2,0,2,-1,1,true,1`,
			simpleAggregation: `{
  "add_line.c":
  {
		"HitCounts":{"2": 1},
		"FileExists": true
  }
}`,
			baseRepo:   "git://repo",
			baseCommit: "commit2",
		},
		{
			name:    "instrumented lines w/o coverage are reported",
			workdir: testsPath + "/all/test-workdir-covermerger",
			bqTable: `timestamp,version,fuzzing_minutes,arch,build_id,manager,kernel_repo,kernel_branch,kernel_commit,file_path,func_name,sl,sc,el,ec,hit_count,inline,pc
samp_time,1,360,arch,b1,ci-mock,git://repo,master,commit1,not_changed.c,func1,3,0,3,-1,0,true,1
samp_time,1,360,arch,b1,ci-mock,git://repo,master,commit2,not_changed.c,func1,4,0,4,-1,0,true,1`,
			simpleAggregation: `{
  "not_changed.c":
  {
		"HitCounts":{"3": 0, "4": 0},
		"FileExists": true
  }
}`,
			baseRepo:   "git://repo",
			baseCommit: "commit2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			aggregation, err := MergeCSVData(
				&Config{
					Jobs:          2,
					Workdir:       test.workdir,
					skipRepoClone: true,
					Base: RepoCommit{
						Repo:   test.baseRepo,
						Commit: test.baseCommit,
					},
					FileVersProvider: &fileVersProviderMock{},
				},
				strings.NewReader(test.bqTable),
			)
			assert.Nil(t, err)
			var expectedAggregation map[string]*MergeResult
			assert.Nil(t, json.Unmarshal([]byte(test.simpleAggregation), &expectedAggregation))
			assert.Equal(t, expectedAggregation, aggregation)
		})
	}
}

type fileVersProviderMock struct{}

func (m *fileVersProviderMock) GetFileVersions(c *Config, targetFilePath string, repoCommits []RepoCommit,
) (fileVersions, error) {
	res := make(fileVersions)
	for _, repoCommit := range repoCommits {
		filePath := filepath.Join(c.Workdir, "repos", repoCommit.Commit, targetFilePath)
		if bytes, err := os.ReadFile(filePath); err == nil {
			res[repoCommit] = string(bytes)
		}
	}
	return res, nil
}

func readFileOrFail(t *testing.T, path string) string {
	absPath, err := filepath.Abs(path)
	assert.Nil(t, err)
	content, err := os.ReadFile(absPath)
	assert.Nil(t, err)
	return string(content)
}
