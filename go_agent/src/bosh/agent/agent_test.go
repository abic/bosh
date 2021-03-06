package agent

import (
	testaction "bosh/agent/action/testhelpers"
	boshtask "bosh/agent/task"
	testtask "bosh/agent/task/testhelpers"
	boshmbus "bosh/mbus"
	testmbus "bosh/mbus/testhelpers"
	boshstats "bosh/platform/stats"
	teststats "bosh/platform/stats/testhelpers"
	testplatform "bosh/platform/testhelpers"
	boshsettings "bosh/settings"
	"encoding/json"
	"errors"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestRunRespondsWithExceptionWhenTheMethodIsUnknown(t *testing.T) {
	req := boshmbus.NewRequest("reply to me", "gibberish", []byte{})

	settings, handler, platform, taskService, actionFactory := getAgentDependencies()
	agent := New(settings, handler, platform, taskService, actionFactory)

	err := agent.Run()
	assert.NoError(t, err)
	assert.True(t, handler.ReceivedRun)

	resp := handler.Func(req)

	respBytes, _ := json.Marshal(resp)
	assert.Equal(t, `{"exception":{"message":"unknown message gibberish"}}`, string(respBytes))
}

func TestRunHandlesPingMessage(t *testing.T) {
	req := boshmbus.NewRequest("reply to me!", "ping", []byte("some payload"))
	assertRequestIsProcessedSynchronously(t, req)
}

func TestRunHandlesGetTaskMessage(t *testing.T) {
	req := boshmbus.NewRequest("reply to me!", "get_task", []byte(`{"arguments":["57"]}`))
	assertRequestIsProcessedSynchronously(t, req)
}

func TestRunHandlesGetStateMessage(t *testing.T) {
	req := boshmbus.NewRequest("reply to me!", "get_state", []byte(`{}`))
	assertRequestIsProcessedSynchronously(t, req)
}

func assertRequestIsProcessedSynchronously(t *testing.T, req boshmbus.Request) {
	settings, handler, platform, taskService, actionFactory := getAgentDependencies()

	// when action is successful
	actionFactory.CreateAction = &testaction.TestAction{
		RunValue: "some value",
	}

	agent := New(settings, handler, platform, taskService, actionFactory)

	err := agent.Run()
	assert.NoError(t, err)
	assert.True(t, handler.ReceivedRun)

	resp := handler.Func(req)
	assert.Equal(t, req.Method, actionFactory.CreateMethod)
	assert.Equal(t, req.GetPayload(), actionFactory.CreateAction.RunPayload)
	assert.Equal(t, boshmbus.NewValueResponse("some value"), resp)

	// when action returns an error
	actionFactory.CreateAction = &testaction.TestAction{
		RunErr: errors.New("some error"),
	}

	agent = New(settings, handler, platform, taskService, actionFactory)
	agent.Run()

	resp = handler.Func(req)
	respBytes, _ := json.Marshal(resp)
	assert.Equal(t, `{"exception":{"message":"some error"}}`, string(respBytes))
}

func TestRunHandlesApplyMessage(t *testing.T) {
	req := boshmbus.NewRequest("reply to me!", "apply", []byte("some payload"))
	assertRequestIsProcessedAsynchronously(t, req)
}

func assertRequestIsProcessedAsynchronously(t *testing.T, req boshmbus.Request) {
	settings, handler, platform, taskService, actionFactory := getAgentDependencies()

	taskService.StartTaskStartedTask = boshtask.Task{Id: "found-57-id", State: boshtask.TaskStateDone}
	actionFactory.CreateAction = new(testaction.TestAction)

	agent := New(settings, handler, platform, taskService, actionFactory)

	err := agent.Run()
	assert.NoError(t, err)
	assert.True(t, handler.ReceivedRun)

	resp := handler.Func(req)
	assert.Equal(t, boshmbus.NewValueResponse(TaskValue{AgentTaskId: "found-57-id", State: boshtask.TaskStateDone}), resp)

	valueBytes, _ := json.Marshal(resp)
	assert.Equal(t, `{"value":{"agent_task_id":"found-57-id","state":"done"}}`, string(valueBytes))

	taskService.StartTaskFunc()
	assert.Equal(t, req.Method, actionFactory.CreateMethod)
	assert.Equal(t, req.GetPayload(), actionFactory.CreateAction.RunPayload)
}

func TestRunSetsUpHeartbeats(t *testing.T) {
	settings, handler, platform, taskService, actionFactory := getAgentDependencies()
	settings.Disks = boshsettings.Disks{
		System:     "/dev/sda1",
		Ephemeral:  "/dev/sdb",
		Persistent: map[string]string{"vol-xxxx": "/dev/sdf"},
	}

	platform.FakeStatsCollector = &teststats.FakeStatsCollector{
		CpuLoad:   boshstats.CpuLoad{One: 1.0, Five: 5.0, Fifteen: 15.0},
		CpuStats:  boshstats.CpuStats{User: 55, Sys: 44, Wait: 11, Total: 1000},
		MemStats:  boshstats.MemStats{Used: 40 * 1024 * 1024, Total: 100 * 1024 * 1024},
		SwapStats: boshstats.MemStats{Used: 10 * 1024 * 1024, Total: 100 * 1024 * 1024},
		DiskStats: map[string]boshstats.DiskStats{
			"/":               boshstats.DiskStats{Used: 25, Total: 100, InodeUsed: 300, InodeTotal: 1000},
			"/var/vcap/data":  boshstats.DiskStats{Used: 5, Total: 100, InodeUsed: 150, InodeTotal: 1000},
			"/var/vcap/store": boshstats.DiskStats{Used: 0, Total: 100, InodeUsed: 0, InodeTotal: 1000},
		},
	}

	agent := New(settings, handler, platform, taskService, actionFactory)
	agent.heartbeatInterval = 5 * time.Millisecond
	err := agent.Run()
	assert.NoError(t, err)

	var hb boshmbus.Heartbeat

	select {
	case hb = <-handler.HeartbeatChan:
	case <-time.After(time.Millisecond):
		t.Errorf("Did not receive an initial heartbeat in time")
	}

	select {
	case hb = <-handler.HeartbeatChan:
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Did not receive a second heartbeat in time")
	}

	assert.Equal(t, []string{"1.00", "5.00", "15.00"}, hb.Vitals.CpuLoad)

	assert.Equal(t, boshmbus.CpuStats{
		User: "5.5",
		Sys:  "4.4",
		Wait: "1.1",
	}, hb.Vitals.Cpu)

	assert.Equal(t, boshmbus.MemStats{
		Percent: "40",
		Kb:      "40960",
	}, hb.Vitals.UsedMem)

	assert.Equal(t, boshmbus.MemStats{
		Percent: "10",
		Kb:      "10240",
	}, hb.Vitals.UsedSwap)

	assert.Equal(t, boshmbus.Disks{
		System:     boshmbus.DiskStats{Percent: "25", InodePercent: "30"},
		Ephemeral:  boshmbus.DiskStats{Percent: "5", InodePercent: "15"},
		Persistent: boshmbus.DiskStats{Percent: "0", InodePercent: "0"},
	}, hb.Vitals.Disks)
}

func TestRunSetsUpHeartbeatsWithoutEphemeralOrPersistentDisk(t *testing.T) {
	settings, handler, platform, taskService, actionFactory := getAgentDependencies()
	settings.Disks = boshsettings.Disks{
		System: "/dev/sda1",
	}

	platform.FakeStatsCollector = &teststats.FakeStatsCollector{
		DiskStats: map[string]boshstats.DiskStats{
			"/":               boshstats.DiskStats{Used: 25, Total: 100, InodeUsed: 300, InodeTotal: 1000},
			"/var/vcap/data":  boshstats.DiskStats{Used: 5, Total: 100, InodeUsed: 150, InodeTotal: 1000},
			"/var/vcap/store": boshstats.DiskStats{Used: 0, Total: 100, InodeUsed: 0, InodeTotal: 1000},
		},
	}

	agent := New(settings, handler, platform, taskService, actionFactory)
	agent.heartbeatInterval = time.Millisecond
	err := agent.Run()
	assert.NoError(t, err)

	hb := <-handler.HeartbeatChan

	assert.Equal(t, boshmbus.Disks{
		System:     boshmbus.DiskStats{Percent: "25", InodePercent: "30"},
		Ephemeral:  boshmbus.DiskStats{},
		Persistent: boshmbus.DiskStats{},
	}, hb.Vitals.Disks)
}

func getAgentDependencies() (
	settings boshsettings.Settings,
	handler *testmbus.FakeHandler,
	platform *testplatform.FakePlatform,
	taskService *testtask.FakeService,
	actionFactory *testaction.FakeFactory) {

	settings = boshsettings.Settings{}
	handler = &testmbus.FakeHandler{}
	platform = &testplatform.FakePlatform{
		FakeStatsCollector: &teststats.FakeStatsCollector{},
	}
	taskService = &testtask.FakeService{}
	actionFactory = &testaction.FakeFactory{}
	return
}
