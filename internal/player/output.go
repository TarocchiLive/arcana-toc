package player

// deviceOutput 独占一个连接；close 中断所有 I/O，wait 回收输出任务。
type deviceOutput interface {
	start(*beepHandle)
	close()
	wait()
}
