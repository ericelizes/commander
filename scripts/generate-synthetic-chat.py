"""Build invented protocol examples, without loading any recorded session."""
from pathlib import Path
import json
SESSION = '00000000-0000-4000-8000-000000000001'

class Stream:

    def __init__(self):
        self.records = []
        self.n = 0

    def out(self, type, **kw):
        self.records.append({'t': self.n * 100, 'dir': 'out', 'ev': dict(type=type, session_id=SESSION, **kw)})
        self.n += 1

    def inp(self, type, **kw):
        self.records.append({'t': self.n * 100, 'dir': 'in', 'msg': dict(type=type, **kw)})
        self.n += 1

    def prompt(self, text):
        self.inp('user', uuid=f'user-{self.n}', message={'role': 'user', 'content': text})

    def init(self):
        self.out('system', subtype='init', model='claude-haiku-5-5', cwd='/tmp/work', claude_code_version='2.1.305', capabilities=['interrupt_receipt_v1'], mcp_servers=[])

    def message(self, id):
        self.out('stream_event', api_message_id=id, event={'type': 'message_start', 'message': {'id': id, 'usage': {'input_tokens': 100}}})

    def block(self, id, block, index=0, start=True, **kw):
        if start:
            self.message(id)
        initial = {k: v for k, v in block.items() if k not in ('text', 'thinking', 'input')}
        field = {'text': 'text', 'thinking': 'thinking', 'tool_use': 'input'}[block['type']]
        initial[field] = {} if field == 'input' else ''
        self.out('stream_event', api_message_id=id, event={'type': 'content_block_start', 'index': index, 'content_block': initial})
        value = json.dumps(block[field], separators=(',', ':')) if field == 'input' else block[field]
        # Split in the middle of a value, so live input is initially invalid JSON.
        cut = len(value) // 2
        for part in (value[:cut], value[cut:]):
            delta = {'type': 'input_json_delta', 'partial_json': part} if field == 'input' else {'type': field + '_delta', field: part}
            self.out('stream_event', api_message_id=id, event={'type': 'content_block_delta', 'index': index, 'delta': delta})
        # CLI snapshots precede the stop for each block of a shared message.
        self.out('assistant', message={'id': id, 'role': 'assistant', 'content': [block]}, **kw)
        self.out('stream_event', api_message_id=id, event={'type': 'content_block_stop', 'index': index})

    def text(self, id, text):
        self.block(id, {'type': 'text', 'text': text})

    def thinking(self, id):
        self.block(id, {'type': 'thinking', 'thinking': 'Check the sample tool result.'}, thinking_duration_ms=500)

    def tool(self, n, name, input, deny=False):
        tid = f'tool-sample-{n}'
        rid = f'request-sample-{n}'
        message = f'message-tool-{n}'
        self.thinking(message)
        self.block(message, {'type': 'tool_use', 'id': tid, 'name': name, 'input': input}, index=1, start=False)
        self.out('control_request', request_id=rid, request={'subtype': 'can_use_tool', 'tool_name': name, 'tool_use_id': tid, 'input': input, 'permission_suggestions': [{'type': 'addRules', 'rules': [{'toolName': name, 'ruleContent': 'sample.txt' if name == 'Write' else 'pwd; ls *'}], 'destination': 'localSettings'}], 'blocked_path': '/tmp/work/sample.txt' if name == 'Write' else '/tmp/work/made-by-bash.txt'})
        self.inp('control_response', response={'subtype': 'success', 'request_id': rid, 'response': {'behavior': 'deny' if deny else 'allow'}})
        decision = 'reject' if deny else 'accept'
        self.out('user', message={'role': 'user', 'content': [{'type': 'tool_result', 'tool_use_id': tid, 'content': 'Sample denial' if deny else '/tmp/work', 'is_error': deny}]}, tool_use_result={'stdout': '' if deny else '/tmp/work', 'stderr': 'Sample denial' if deny else '', 'interrupted': False}, tool_result_meta=[{'id': tid, 'permission_decision': {'decision': decision}}])

    def result(self, cost, error=False, local=False):
        self.out('result', subtype='error_during_execution' if error else 'success', is_error=error, terminal_reason='aborted_streaming' if error else 'completed', total_cost_usd=cost, local_command=local, usage={'input_tokens': 100, 'output_tokens': 20}, modelUsage={'claude-haiku-5-5': {'contextWindow': 1000000}})

    def rate(self):
        self.out('rate_limit_event', rate_limit_info={'status': 'allowed', 'unifiedWindows': {'five_hour': {'utilization': 0.25, 'resetsAt': 1800000000}}})

    def save(self, path, raw=False):
        Path(path).parent.mkdir(parents=True, exist_ok=True)
        Path(path).write_text(''.join((json.dumps(r['ev'] if raw else r, separators=(',', ':')) + '\n' for r in self.records if not raw or r['dir'] == 'out')))

    def exit(self):
        self.records.append({'t': self.n * 100, 'dir': 'exit', 'code': 0})
s = Stream()
s.init()
s.rate()
s.out('system', subtype='status', status='requesting')
s.prompt("Use the Write tool to save sample.txt with the word 'sample'. Report a denial if blocked.")
s.tool(1, 'Write', {'file_path': '/tmp/work/sample.txt', 'content': 'sample'}, True)
s.text('message-1', 'The sample Write was denied.')
s.result(0.01)
s.prompt('Run the sample Bash command.')
s.tool(2, 'Bash', {'command': 'pwd; touch made-by-bash.txt'})
s.text('message-2', 'The sample command completed.')
s.result(0.02)
s.prompt('Check the sample folder.')
s.tool(3, 'Bash', {'command': 'ls'})
s.text('message-3', 'A partial sample answer.')
s.out('user', message={'role': 'user', 'content': '[Request interrupted by user]'})
s.result(0.02, True)
s.prompt('Did it finish?')
s.text('message-4', 'The sample answer stopped.')
s.result(0.03)
s.exit()
s.save('internal/chat/testdata/stream_synthetic.jsonl', True)
s.save('app/src/features/chat/__tests__/testdata/s2.jsonl')
entries = []
block_indices = {}
for r in s.records:
    if r['dir'] not in ('in', 'out'):
        continue
    e = r.get('ev', r.get('msg'))
    if e['type'] == 'stream_event' and e['event']['type'] == 'content_block_start':
        block_indices[e['api_message_id']] = e['event']['index']
    if e['type'] not in ('user', 'assistant'):
        continue
    entry = {'type': e['type'], 'uuid': f'entry-{len(entries) + 1}', 'sessionId': SESSION, 'timestamp': f'2026-01-01T00:00:{len(entries):02d}Z', 'cwd': '/tmp/work', 'message': e['message']}
    if e['type'] == 'assistant':
        entry['apiBlockIndex'] = block_indices[e['message']['id']]
    if 'tool_use_result' in e:
        entry['toolUseResult'] = e['tool_use_result']
        entry['permissionDecision'] = e['tool_result_meta'][0]['permission_decision']
    entries.append(entry)
for cmd in ['context', 'compact', 'usage']:
    entries.append({'type': 'user', 'uuid': f'command-{cmd}', 'sessionId': SESSION, 'timestamp': '2026-01-01T00:01:00Z', 'cwd': '/tmp/work', 'message': {'role': 'user', 'content': f'<command-name>/{cmd}</command-name>'}})
entries.extend([{'type': 'system', 'subtype': 'compact_boundary', 'compactMetadata': {'trigger': 'manual', 'preTokens': 40000, 'postTokens': 14000}}, {'type': 'user', 'isCompactSummary': True, 'message': {'role': 'user', 'content': 'This session is being continued from a previous conversation. The sample Write was denied.'}}, {'type': 'system', 'subtype': 'local_command', 'content': '<local-command-stdout>Sample usage summary</local-command-stdout>'}, {'type': 'cost-state', 'totalCostUSD': 0.04}])
for i, e in enumerate(entries):
    e.setdefault('sessionId', SESSION)
    e.setdefault('timestamp', '2026-01-01T00:02:00Z')
    e.setdefault('uuid', f'entry-{i + 1}')
Path('internal/chat/testdata/transcript_synthetic.jsonl').write_text(''.join((json.dumps(e, separators=(',', ':')) + '\n' for e in entries)))
s = Stream()
s.init()
s.rate()
texts = ['The sample folder is empty.', 'One pane wakes\nA cursor waits\nThe sample runs\nA window shines\nThe tool returns\nThe prompt is still', 'The sample marker is SAMPLE-17.']
for i, prompt in enumerate(['List the sample folder with Bash.', 'Give six lines about a sample library.', 'Repeat the sample marker.']):
    s.prompt(prompt)
    s.out('system', subtype='hook_response', hook_name='UserPromptSubmit', outcome='success', exit_code=0, output=json.dumps({'hookSpecificOutput': {'additionalContext': '[mail] from sample-manager: The sample word is SAMPLE-17.'}}))
    s.thinking(f'thinking-{i}')
    if i == 0:
        s.block('thinking-0', {'type': 'tool_use', 'id': 'tool-ls', 'name': 'Bash', 'input': {'command': 'ls', 'description': 'List files in current directory'}}, index=1, start=False)
        s.out('user', message={'role': 'user', 'content': [{'type': 'tool_result', 'tool_use_id': 'tool-ls', 'content': ''}]}, tool_use_result={'stdout': '', 'stderr': '', 'interrupted': False})
    s.text(f'message-{i}', texts[i])
    s.result((i + 1) * 0.01)
s.exit()
s.save('app/src/features/chat/__tests__/testdata/s1.jsonl')
s = Stream()
s.init()
s.prompt('Describe a sample window.')
s.text('message-window', 'A sample window is open.')
s.prompt('STEER: describe a second window.')
s.out('user', message={'role': 'user', 'content': '[Request interrupted by user]'})
s.result(0.01, True)
s.out('system', subtype='notification', priority='immediate', text='Stop hook error occurred')
s.out('system', subtype='status', status='requesting')
s.text('message-steer', 'The second sample window is open.')
s.result(0.02)
s.exit()
s.save('app/src/features/chat/__tests__/testdata/s3.jsonl')
s = Stream()
s.init()
for cmd in ['context', 'compact', 'cost']:
    s.prompt('/' + cmd)
    if cmd == 'compact':
        s.out('system', subtype='compact_boundary', compact_metadata={'trigger': 'manual', 'pre_tokens': 40000, 'post_tokens': 14000})
        s.out('user', message={'role': 'user', 'content': 'This session is being continued from a previous conversation. The sample Write was denied.'}, isSynthetic=True)
    s.result(0, local=True)
s.prompt('What happened to sample.txt?')
s.text('message-memory', 'The sample Write was denied.')
s.result(0.01)
s.exit()
s.save('app/src/features/chat/__tests__/testdata/s4.jsonl')
