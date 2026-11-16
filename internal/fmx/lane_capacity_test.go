package fmx

import (
	"testing"
)

func TestValidateLaneCapacityAccepts100Agents(t *testing.T) {
	cfg := LaneConfig{Agents: 100}
	samples := []LaneLoadSample{
		{Active: 100},
		{Active: 100},
		{Active: 100},
	}
	err := ValidateLaneCapacity(cfg, samples)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestValidateLaneCapacityRejects99Agents(t *testing.T) {
	cfg := LaneConfig{Agents: 99}
	samples := []LaneLoadSample{
		{Active: 100},
	}
	err := ValidateLaneCapacity(cfg, samples)
	if err != ErrBelowAgentFloor {
		t.Errorf("expected ErrBelowAgentFloor, got %v", err)
	}
}

func TestValidateLaneCapacityRejectsDipInSamples(t *testing.T) {
	cfg := LaneConfig{Agents: 120}
	samples := []LaneLoadSample{
		{Active: 120},
		{Active: 80},
		{Active: 120},
	}
	err := ValidateLaneCapacity(cfg, samples)
	if err != ErrNotContinuous {
		t.Errorf("expected ErrNotContinuous, got %v", err)
	}
}

func TestValidateLaneCapacityRejectsNoSamples(t *testing.T) {
	cfg := LaneConfig{Agents: 120}
	samples := []LaneLoadSample{}
	err := ValidateLaneCapacity(cfg, samples)
	if err != ErrNotContinuous {
		t.Errorf("expected ErrNotContinuous, got %v", err)
	}
}
