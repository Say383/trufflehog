package gitlab

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sync/errgroup"

	"github.com/trufflesecurity/trufflehog/v3/pkg/context"
	"github.com/trufflesecurity/trufflehog/v3/pkg/sources/git"
)

func Test_setProgressCompleteWithRepo_resumeInfo(t *testing.T) {
	tests := []struct {
		startingResumeInfoSlice []string
		repoURL                 string
		wantResumeInfoSlice     []string
	}{
		{
			startingResumeInfoSlice: []string{},
			repoURL:                 "a",
			wantResumeInfoSlice:     []string{"a"},
		},
		{
			startingResumeInfoSlice: []string{"b"},
			repoURL:                 "a",
			wantResumeInfoSlice:     []string{"a", "b"},
		},
	}

	s := &Source{repos: []string{}}

	for _, tt := range tests {
		s.resumeInfoSlice = tt.startingResumeInfoSlice
		s.setProgressCompleteWithRepo(0, 0, tt.repoURL)
		if !reflect.DeepEqual(s.resumeInfoSlice, tt.wantResumeInfoSlice) {
			t.Errorf("s.setProgressCompleteWithRepo() got: %v, want: %v", s.resumeInfoSlice, tt.wantResumeInfoSlice)
		}
	}
}

func Test_setProgressCompleteWithRepo_Progress(t *testing.T) {
	repos := []string{"a", "b", "c", "d", "e"}
	tests := map[string]struct {
		repos                 []string
		index                 int
		offset                int
		wantPercentComplete   int64
		wantSectionsCompleted int32
		wantSectionsRemaining int32
	}{
		"starting from the beginning, no offset": {
			repos:                 repos,
			index:                 0,
			offset:                0,
			wantPercentComplete:   0,
			wantSectionsCompleted: 0,
			wantSectionsRemaining: 5,
		},
		"resume from the third, offset 2": {
			repos:                 repos[2:],
			index:                 0,
			offset:                2,
			wantPercentComplete:   40,
			wantSectionsCompleted: 2,
			wantSectionsRemaining: 5,
		},
		"resume from the third, on last repo, offset 2": {
			repos:                 repos[2:],
			index:                 2,
			offset:                2,
			wantPercentComplete:   80,
			wantSectionsCompleted: 4,
			wantSectionsRemaining: 5,
		},
	}

	for _, tt := range tests {
		s := &Source{
			repos: tt.repos,
		}

		s.setProgressCompleteWithRepo(tt.index, tt.offset, "")
		gotProgress := s.GetProgress()
		if gotProgress.PercentComplete != tt.wantPercentComplete {
			t.Errorf("s.setProgressCompleteWithRepo() PercentComplete got: %v want: %v", gotProgress.PercentComplete, tt.wantPercentComplete)
		}
		if gotProgress.SectionsCompleted != tt.wantSectionsCompleted {
			t.Errorf("s.setProgressCompleteWithRepo() PercentComplete got: %v want: %v", gotProgress.SectionsCompleted, tt.wantSectionsCompleted)
		}
		if gotProgress.SectionsRemaining != tt.wantSectionsRemaining {
			t.Errorf("s.setProgressCompleteWithRepo() PercentComplete got: %v want: %v", gotProgress.SectionsRemaining, tt.wantSectionsRemaining)
		}
	}
}

func Test_scanRepos_SetProgressComplete(t *testing.T) {
	testCases := []struct {
		name         string
		repos        []string
		wantComplete bool
		wantErr      bool
	}{
		{
			name:         "no repos",
			wantComplete: true,
		},
		{
			name:         "one valid repo",
			repos:        []string{"repo"},
			wantComplete: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			src := &Source{
				repos: tc.repos,
			}
			src.jobPool = &errgroup.Group{}
			src.scanOptions = &git.ScanOptions{}

			_ = src.scanRepos(context.Background(), nil)
			if !tc.wantErr {
				assert.Equal(t, "", src.GetProgress().EncodedResumeInfo)
			}

			gotComplete := src.GetProgress().PercentComplete == 100
			if gotComplete != tc.wantComplete {
				t.Errorf("got: %v, want: %v", gotComplete, tc.wantComplete)
			}
		})
	}
}
