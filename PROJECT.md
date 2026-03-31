# 目标
1. 根据[weibo_monitor_go](weibo_monitor_go)项目以及[group_chat_monitor](group_chat_monitor)项目，实现全新的项目 weibo_group_chat_monitor
2. weibo_monitor_go的功能保持不变，group_chat_monitor的功能在现有的基础上增加数据的规整
3. group_chat_monitor的功能目标为用于设定在每天的固定时间点，根据之前抓取点对微信群进行全量的信息抓取，获取群聊记录，并筛选特定发送者的消息并推送到telegram。
4. telegram消息格式为："当前年月日 \n [用户名]发送了[数量]条消息，分别是：\n [时间] [消息内容]\n [时间] [消息内容]\n ...\n [时间] [消息内容]" 如果存在图片内容，则在消息内容后添加"[图片]"
5. 两个功能用于独立的命令行参数运行
