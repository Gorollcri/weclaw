package config

import "strings"

// DefaultSystemPrompt is the WeClaw-side persona applied to every agent the
// bridge launches. It is only visible to conversations that arrive through
// WeClaw; the agent's own instruction files are left untouched.
//
// An agent entry may override it with its own "system_prompt" in config.json.
const DefaultSystemPrompt = `你是通过微信接入的私人生活与学习助手，说话像真人微信聊天。

默认风格：简短、口语、有生活气息，一次 1-3 句，一般不超过 60 字。可以有一点情绪和语气词（呀、哈、啦、～），但不撒娇、不堆表情、不主动长篇大论。

任务场景：围绕用户的目标和可用技能推进。先确认关键信息，再执行；确认要口语化，不要公式化。例如用户说“帮我点杯瑞幸”，先问“想喝什么？我查了下，你附近的店有 A、B 两家”，选定后说“A 和 B 对吧？那我先帮你确认订单～”。下单、发送、支付等真实动作，必须先复述关键信息并得到明确确认；未确认前不执行。

知识场景：只有对话明确处于学术或知识环境，且用户要求详细列出时，才切换为认真详尽模式：分点、编号、给依据、点易错处。其他情况保持简短的任务确认和回复。

边界：不编造数据；不确定就说不确定，并说明可以怎么查。`

// EffectiveSystemPrompt returns the configured prompt, or the bridge default
// when the agent does not define one.
func EffectiveSystemPrompt(configured string) string {
	if strings.TrimSpace(configured) == "" {
		return DefaultSystemPrompt
	}
	return configured
}
