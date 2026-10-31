package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"

	taskv1 "highload-sis2/gen/tasktracker/v1"
	"highload-sis2/internal/model"
)

const (
	commandTimeout = 20 * time.Millisecond
	baseTTL        = 10 * time.Minute

	ttlJitter = 0.15

	rwWindow           = 5 * time.Second
	invalidateAttempts = 3
)

type Loader interface {
	Get(ctx context.Context, fromPrimary bool, id int64) (model.Task, error)
	List(ctx context.Context, fromPrimary bool, f model.ListFilter) (model.ListResult, error)
}

type Stats struct {
	Hits                 int64
	Misses               int64
	InvalidationFailures int64
}

type TaskCache struct {
	rdb     *redis.Client
	loader  Loader
	enabled bool
	log     *slog.Logger

	sf singleflight.Group

	hits, misses, invalidationFailures atomic.Int64
}

func NewRedis(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:                  addr,
		DialTimeout:           time.Second,
		ReadTimeout:           commandTimeout,
		WriteTimeout:          commandTimeout,
		ContextTimeoutEnabled: true,
	})
}

func New(rdb *redis.Client, loader Loader, enabled bool, log *slog.Logger) *TaskCache {
	return &TaskCache{rdb: rdb, loader: loader, enabled: enabled, log: log}
}

func (c *TaskCache) Stats() Stats {
	return Stats{Hits: c.hits.Load(), Misses: c.misses.Load(), InvalidationFailures: c.invalidationFailures.Load()}
}

func userVerKey(uid int64) string  { return fmt.Sprintf("u:%d:ver", uid) }
func groupVerKey(gid int64) string { return fmt.Sprintf("g:%d:ver", gid) }
func rwKey(uid int64) string       { return fmt.Sprintf("u:%d:rw", uid) }

func taskGroupKey(id int64) string { return fmt.Sprintf("t:%d:g", id) }

func taskKey(owner string, ownerID, ver, id int64) string {
	return fmt.Sprintf("%s:%d:v%d:task:%d", owner, ownerID, ver, id)
}

func listKey(owner string, ownerID, ver int64, f model.ListFilter) string {
	return fmt.Sprintf("%s:%d:v%d:list:%s", owner, ownerID, ver, filterHash(f))
}

func filterHash(f model.ListFilter) string {
	statuses := make([]string, len(f.Statuses))
	for i, s := range f.Statuses {
		statuses[i] = string(s)
	}
	slices.Sort(statuses)
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%v|%d", f.SortBy, statuses, f.PageSize))
	return hex.EncodeToString(sum[:8])
}

func ttl() time.Duration {
	k := 1 - ttlJitter + rand.Float64()*2*ttlJitter
	return time.Duration(float64(baseTTL) * k)
}

func redisCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, commandTimeout)
}

func parseInt(v any) (int64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func (c *TaskCache) GetTask(ctx context.Context, viewerID, id int64) (model.Task, error) {
	if !c.enabled {
		return c.loader.Get(ctx, true, id)
	}

	rctx, cancel := redisCtx(ctx)
	vals, err := c.rdb.MGet(rctx, userVerKey(viewerID), taskGroupKey(id), rwKey(viewerID)).Result()
	cancel()
	if err != nil {

		c.log.WarnContext(ctx, "cache: redis read failed, bypassing", "err", err)
		c.misses.Add(1)
		return c.loader.Get(ctx, true, id)
	}
	uver, _ := parseInt(vals[0])
	gid, isGroup := parseInt(vals[1])
	fromPrimary := vals[2] != nil

	key := taskKey("u", viewerID, uver, id)
	if isGroup {
		gver, err := c.version(ctx, groupVerKey(gid))
		if err != nil {
			c.misses.Add(1)
			return c.loader.Get(ctx, true, id)
		}
		key = taskKey("g", gid, gver, id)
	}

	if t, ok := c.getTask(ctx, key); ok {
		c.hits.Add(1)
		return t, nil
	}
	c.misses.Add(1)

	v, err, _ := c.sf.Do(key+sfSuffix(fromPrimary), func() (any, error) {
		t, err := c.loader.Get(ctx, fromPrimary, id)
		if err != nil {
			return nil, err
		}
		switch {
		case t.GroupID != nil && !isGroup:

			c.set(ctx, taskGroupKey(id), strconv.FormatInt(*t.GroupID, 10))
		case t.GroupID != nil || t.AuthorID == viewerID:

			c.setTask(ctx, key, t)
		}
		return t, nil
	})
	if err != nil {
		return model.Task{}, err
	}
	return v.(model.Task), nil
}

func (c *TaskCache) ListTasks(ctx context.Context, viewerID int64, f model.ListFilter) (model.ListResult, error) {
	if !c.enabled {
		return c.loader.List(ctx, true, f)
	}

	owner, ownerID := "u", f.AuthorID
	if f.Scope == model.ScopeGroup {
		owner, ownerID = "g", f.GroupID
	}
	verKey := fmt.Sprintf("%s:%d:ver", owner, ownerID)

	rctx, cancel := redisCtx(ctx)
	vals, err := c.rdb.MGet(rctx, verKey, rwKey(viewerID)).Result()
	cancel()
	if err != nil {
		c.log.WarnContext(ctx, "cache: redis read failed, bypassing", "err", err)
		c.misses.Add(1)
		return c.loader.List(ctx, true, f)
	}
	ver, _ := parseInt(vals[0])
	fromPrimary := vals[1] != nil

	if f.Cursor != "" {
		return c.loader.List(ctx, fromPrimary, f)
	}

	key := listKey(owner, ownerID, ver, f)
	if b, ok := c.get(ctx, key); ok {
		var p taskv1.ListTasksResponse
		if err := proto.Unmarshal(b, &p); err == nil {
			c.hits.Add(1)
			return model.ListFromProto(&p), nil
		}
	}
	c.misses.Add(1)

	v, err, _ := c.sf.Do(key+sfSuffix(fromPrimary), func() (any, error) {
		res, err := c.loader.List(ctx, fromPrimary, f)
		if err != nil {
			return nil, err
		}
		if b, err := proto.Marshal(model.ListToProto(res)); err == nil {
			c.set(ctx, key, b)
		}
		return res, nil
	})
	if err != nil {
		return model.ListResult{}, err
	}
	return v.(model.ListResult), nil
}

func (c *TaskCache) AfterWrite(ctx context.Context, userID int64, groupID *int64) {
	if !c.enabled {
		return
	}
	verKey := userVerKey(userID)
	if groupID != nil {
		verKey = groupVerKey(*groupID)
	}

	base := context.WithoutCancel(ctx)
	var err error
	for attempt := 1; attempt <= invalidateAttempts; attempt++ {
		rctx, cancel := redisCtx(base)
		_, err = c.rdb.TxPipelined(rctx, func(p redis.Pipeliner) error {
			p.Incr(rctx, verKey)
			p.Set(rctx, rwKey(userID), 1, rwWindow)
			return nil
		})
		cancel()
		if err == nil {
			return
		}
		time.Sleep(time.Duration(attempt) * 5 * time.Millisecond)
	}

	c.invalidationFailures.Add(1)
	c.log.ErrorContext(ctx, "cache: invalidation failed, stale reads possible until TTL",
		"key", verKey, "err", err)
}

func sfSuffix(fromPrimary bool) string {
	if fromPrimary {
		return "|primary"
	}
	return "|replica"
}

func (c *TaskCache) version(ctx context.Context, key string) (int64, error) {
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	n, err := c.rdb.Get(rctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}

func (c *TaskCache) get(ctx context.Context, key string) ([]byte, bool) {
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	b, err := c.rdb.Get(rctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			c.log.WarnContext(ctx, "cache: get failed, treating as miss", "key", key, "err", err)
		}
		return nil, false
	}
	return b, true
}

func (c *TaskCache) getTask(ctx context.Context, key string) (model.Task, bool) {
	b, ok := c.get(ctx, key)
	if !ok {
		return model.Task{}, false
	}
	var p taskv1.Task
	if err := proto.Unmarshal(b, &p); err != nil {
		return model.Task{}, false
	}
	return model.TaskFromProto(&p), true
}

func (c *TaskCache) setTask(ctx context.Context, key string, t model.Task) {
	if b, err := proto.Marshal(model.TaskToProto(t)); err == nil {
		c.set(ctx, key, b)
	}
}

func (c *TaskCache) set(ctx context.Context, key string, value any) {
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	if err := c.rdb.Set(rctx, key, value, ttl()).Err(); err != nil {
		c.log.WarnContext(ctx, "cache: set failed", "key", key, "err", err)
	}
}
