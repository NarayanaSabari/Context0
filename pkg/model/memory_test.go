package model

import (
	"testing"
)

func TestMemoryType_Values(t *testing.T) {
	if MemoryTypeEpisodic != "episodic" {
		t.Error("MemoryTypeEpisodic should be 'episodic'")
	}
	if MemoryTypeSemantic != "semantic" {
		t.Error("MemoryTypeSemantic should be 'semantic'")
	}
	if MemoryTypeProcedural != "procedural" {
		t.Error("MemoryTypeProcedural should be 'procedural'")
	}
}

func TestRelationshipType_Values(t *testing.T) {
	if RelBelongsTo != "belongs_to" {
		t.Error("RelBelongsTo should be 'belongs_to'")
	}
	if RelContains != "contains" {
		t.Error("RelContains should be 'contains'")
	}
	if RelRelatesTo != "relates_to" {
		t.Error("RelRelatesTo should be 'relates_to'")
	}
	if RelSupersedes != "supersedes" {
		t.Error("RelSupersedes should be 'supersedes'")
	}
	if RelCausedBy != "caused_by" {
		t.Error("RelCausedBy should be 'caused_by'")
	}
}
