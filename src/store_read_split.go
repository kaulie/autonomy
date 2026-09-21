package autonomy

// 读写分离：一个 store 可以由两台服务器承担 —— 写（以及「读完就写」的读）走远端主库，
// 「看一看」的读走本地副本（streaming replica）。这不是第二种 engine，而是同一个
// engine 的第二种**连接**：契约（七个端口）、SQL、schema 都不变，变的只是某一次读去哪台
// 服务器取数（见 docs/store.md「读写分离」）。
//
// 分工写在**调用点**上，因为只有调用者知道它要的答案会不会被拿去写：读一眼任务详情
// （HTTP 进度、对话流、数据 API）可以容忍副本的滞后，读一行任务去改它则不行。所以：
//
//   - 默认：读走副本（engine 配了副本时）—— 上层什么都不用写；
//   - `writerReads(port)`：这次读必须是「自己刚写下的那一版」（判决、广播、受理、收件箱
//     自己那条队列……），于是改走主库。
//
// engine 只提供「把同一个 store 换成写侧」这一个动作，不猜语义；上层说清楚它要什么。

// ReadSource says which side of a split store a read is served from.
type ReadSource int

const (
	// ReadFollower is the follower: a local streaming replica of the writing
	// database, when the engine has one configured. Its answer can be behind the
	// writer by the replication lag — which is what makes it cheap, and why only
	// reads that merely observe go here.
	ReadFollower ReadSource = iota
	// ReadWriter is the writing server: reads here see every write this process
	// has already made (read-your-writes).
	ReadWriter
)

// StoreReadSplit is implemented by a store whose reads can be served by a follower
// (a streaming replica of the writing database). An upper-layer caller holds the
// ports, not the store, so it asks through writerReads rather than by naming an
// engine — the same rule as everywhere else: no upper-layer file names a concrete
// engine (store_ports_test.go).
//
// A store with no follower configured has nothing to split and need not implement
// this at all: writerReads leaves it alone.
type StoreReadSplit interface {
	// Reading returns the same store with its reads served as asked. Both answers
	// satisfy every port, so a caller gets back the port it asked with.
	Reading(source ReadSource) Store
}

// writerReads answers with the same port, reading through the **writer**: for a
// read whose answer decides a write, or one that must see a row this process has
// just written. On a store that has one side only (every store without a follower
// configured, including sqlite) it returns s unchanged, so the call is free.
//
// It is the explicit half of the split: reads that do not say this go to the
// follower when there is one (docs/store.md「读写分离」 lists the sites).
func writerReads[T any](s T) T {
	if split, ok := any(s).(StoreReadSplit); ok {
		if writer, ok := split.Reading(ReadWriter).(T); ok {
			return writer
		}
	}
	return s
}
