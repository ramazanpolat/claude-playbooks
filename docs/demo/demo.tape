Output demo.gif
Set FontSize 17
Set Width 940
Set Height 520
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms
Set Shell bash
Set Padding 24
Hide
Type "docker run -it --rm --hostname mac cpb-demo" Enter
Sleep 2s
Type "clear" Enter
Show
Type "# each playbook = a fully isolated Claude Code instance" Sleep 800ms Enter
Sleep 400ms
Type "cpb create writer" Sleep 300ms Enter
Sleep 1.4s
Type "cpb create sre --alias ops" Sleep 300ms Enter
Sleep 1.4s
Type "cpb list" Sleep 300ms Enter
Sleep 1.8s
Type "# just type its name: that playbook opens its OWN Claude" Sleep 800ms Enter
Sleep 300ms
Type "writer" Sleep 400ms Enter
Sleep 2.4s
Type "# separate settings, memory, hooks. your ~/.claude untouched" Sleep 900ms Enter
Sleep 2.2s
