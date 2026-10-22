package daemoncontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/redis/go-redis/v9"
)

var ErrConflict = errors.New("maintenance request conflicts with existing state")

type Store interface {
	Create(context.Context, protocol.MaintenanceDelivery) (*protocol.MaintenanceRequest, error)
	Get(context.Context, string) (*protocol.MaintenanceRequest, error)
	Pending(context.Context, string) (*protocol.MaintenanceRequest, error)
	Deliver(context.Context, protocol.MaintenanceDelivery) (bool, error)
	Complete(context.Context, protocol.MaintenanceResult) error
}

// Delivery is at most once. A lost claim response is indeterminate, not a
// command to redeliver. Local durable receipts independently fence replay.
type RedisStore struct{ client redis.UniversalClient }

func NewRedisStore(client redis.UniversalClient) *RedisStore { return &RedisStore{client: client} }

func requestKey(id string) string { return "mul:{maintenance}:request:" + id }
func activeKey(id string) string  { return "mul:{maintenance}:active:" + id }

var createScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  if redis.call('HGET', KEYS[1], 'delivery') ~= ARGV[1] then return -1 end
  return 0
end
if redis.call('EXISTS', KEYS[2]) == 1 then return -1 end
redis.call('HSET', KEYS[1], 'delivery', ARGV[1], 'state', 'pending')
redis.call('PEXPIRE', KEYS[1], 86400000)
redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
return 1
`)

func (s *RedisStore) Create(ctx context.Context, delivery protocol.MaintenanceDelivery) (*protocol.MaintenanceRequest, error) {
	payload, err := json.Marshal(delivery)
	if err != nil {
		return nil, err
	}
	remaining := time.Until(delivery.Command.IssuedAt.Add(2 * time.Minute)).Milliseconds()
	if remaining <= 0 {
		return nil, ErrConflict
	}
	n, err := createScript.Run(ctx, s.client, []string{requestKey(delivery.Command.ID), activeKey(delivery.Command.DaemonID)},
		string(payload), delivery.Command.ID, remaining).Int()
	if err != nil {
		return nil, fmt.Errorf("persist maintenance command: %w", err)
	}
	if n < 0 {
		return nil, ErrConflict
	}
	return s.Get(ctx, delivery.Command.ID)
}

func (s *RedisStore) Get(ctx context.Context, id string) (*protocol.MaintenanceRequest, error) {
	values, err := s.client.HGetAll(ctx, requestKey(id)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}
	var request protocol.MaintenanceRequest
	if err := protocol.DecodeMaintenanceJSON(strings.NewReader(values["delivery"]), &request.Delivery); err != nil {
		return nil, fmt.Errorf("invalid persisted maintenance command: %w", err)
	}
	request.State = values["state"]
	if request.State != "pending" && request.State != "delivered" && request.State != "completed" {
		return nil, errors.New("invalid maintenance request state")
	}
	if request.State == "completed" {
		var result protocol.MaintenanceResult
		if err := protocol.DecodeMaintenanceJSON(strings.NewReader(values["result"]), &result); err != nil {
			return nil, err
		}
		request.Result = &result
	} else if !time.Now().Before(request.Delivery.Command.IssuedAt.Add(2 * time.Minute)) {
		if request.State == "delivered" {
			request.State = "indeterminate"
		} else {
			request.State = "expired"
		}
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return &request, nil
}

func (s *RedisStore) Pending(ctx context.Context, daemonID string) (*protocol.MaintenanceRequest, error) {
	id, err := s.client.Get(ctx, activeKey(daemonID)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	request, err := s.Get(ctx, id)
	if err != nil || request == nil || request.State != "pending" {
		return nil, err
	}
	return request, nil
}

var deliverScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'delivery') ~= ARGV[1] then return -1 end
if redis.call('HGET', KEYS[1], 'state') ~= 'pending' then return 0 end
redis.call('HSET', KEYS[1], 'state', 'delivered')
return 1
`)

func (s *RedisStore) Deliver(ctx context.Context, delivery protocol.MaintenanceDelivery) (bool, error) {
	payload, err := json.Marshal(delivery)
	if err != nil {
		return false, err
	}
	n, err := deliverScript.Run(ctx, s.client, []string{requestKey(delivery.Command.ID)}, string(payload)).Int()
	if err != nil {
		return false, err
	}
	if n < 0 {
		return false, ErrConflict
	}
	return n == 1, nil
}

var completeScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'delivery') ~= ARGV[1] then return -1 end
local state = redis.call('HGET', KEYS[1], 'state')
if state == 'completed' then
  if redis.call('HGET', KEYS[1], 'result') == ARGV[2] then return 0 end
  return -1
end
if state ~= 'delivered' then return -1 end
redis.call('HSET', KEYS[1], 'state', 'completed', 'result', ARGV[2])
if redis.call('GET', KEYS[2]) == ARGV[3] then redis.call('DEL', KEYS[2]) end
return 1
`)

func (s *RedisStore) Complete(ctx context.Context, result protocol.MaintenanceResult) error {
	delivery, err := json.Marshal(result.Delivery)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	n, err := completeScript.Run(ctx, s.client,
		[]string{requestKey(result.Delivery.Command.ID), activeKey(result.Delivery.Command.DaemonID)},
		string(delivery), string(payload), result.Delivery.Command.ID).Int()
	if err != nil {
		return err
	}
	if n < 0 {
		return ErrConflict
	}
	return nil
}
