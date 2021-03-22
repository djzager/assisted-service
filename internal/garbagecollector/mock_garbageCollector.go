// Code generated by MockGen. DO NOT EDIT.
// Source: garbageCollector.go

// Package garbageCollector is a generated GoMock package.
package garbagecollector

import (
	context "context"
	reflect "reflect"

	strfmt "github.com/go-openapi/strfmt"
	gomock "github.com/golang/mock/gomock"
	s3wrapper "github.com/openshift/assisted-service/pkg/s3wrapper"
	installer "github.com/openshift/assisted-service/restapi/operations/installer"
)

// MockGarbageCollectors is a mock of GarbageCollectors interface.
type MockGarbageCollectors struct {
	ctrl     *gomock.Controller
	recorder *MockGarbageCollectorsMockRecorder
}

// MockGarbageCollectorsMockRecorder is the mock recorder for MockGarbageCollectors.
type MockGarbageCollectorsMockRecorder struct {
	mock *MockGarbageCollectors
}

// NewMockGarbageCollectors creates a new mock instance.
func NewMockGarbageCollectors(ctrl *gomock.Controller) *MockGarbageCollectors {
	mock := &MockGarbageCollectors{ctrl: ctrl}
	mock.recorder = &MockGarbageCollectorsMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockGarbageCollectors) EXPECT() *MockGarbageCollectorsMockRecorder {
	return m.recorder
}

// DeregisterClusterInternal mocks base method.
func (m *MockGarbageCollectors) DeregisterClusterInternal(ctx context.Context, params installer.DeregisterClusterParams) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeregisterClusterInternal", ctx, params)
	ret0, _ := ret[0].(error)
	return ret0
}

// DeregisterClusterInternal indicates an expected call of DeregisterClusterInternal.
func (mr *MockGarbageCollectorsMockRecorder) DeregisterClusterInternal(ctx, params interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeregisterClusterInternal", reflect.TypeOf((*MockGarbageCollectors)(nil).DeregisterClusterInternal), ctx, params)
}

// PermanentClustersDeletion mocks base method.
func (m *MockGarbageCollectors) PermanentClustersDeletion(ctx context.Context, olderThan strfmt.DateTime, objectHandler s3wrapper.API) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PermanentClustersDeletion", ctx, olderThan, objectHandler)
	ret0, _ := ret[0].(error)
	return ret0
}

// PermanentClustersDeletion indicates an expected call of PermanentClustersDeletion.
func (mr *MockGarbageCollectorsMockRecorder) PermanentClustersDeletion(ctx, olderThan, objectHandler interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PermanentClustersDeletion", reflect.TypeOf((*MockGarbageCollectors)(nil).PermanentClustersDeletion), ctx, olderThan, objectHandler)
}