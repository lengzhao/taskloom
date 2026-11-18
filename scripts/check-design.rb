# frozen_string_literal: true
# Static design validation only; not a PluginKit or business behavior test.
require 'yaml'
require 'pathname'

ROOT = File.expand_path('..', __dir__)
Dir.chdir(ROOT)

def assert(condition, message)
  raise message unless condition
end

def deep_merge(base, override)
  return override unless base.is_a?(Hash) && override.is_a?(Hash)
  result = Marshal.load(Marshal.dump(base))
  override.each do |key, value|
    if value.nil?
      result.delete(key)
    else
      result[key] = deep_merge(result[key], value)
    end
  end
  result
end

# Only the subset used in these examples. No claim to implement resolver pruning,
# extends, interpolation, or list append/remove syntax.
def merge_instances(base, override)
  result = Marshal.load(Marshal.dump(base))
  override.each do |id, node|
    previous = result[id]
    same_kind = previous.is_a?(Hash) && node.is_a?(Hash) &&
                (!node.key?('use') || node['use'] == previous['use'])
    result[id] = same_kind ? deep_merge(previous, node) : node
  end
  result
end

# Flatten inline instances only for inspection. This is not a runtime resolver.
def flatten_graph(graph)
  nodes = {}
  edges = {}
  visit = nil
  dependency = nil
  dependency = lambda do |value, path|
    case value
    when String then [value]
    when Array then value.each_with_index.flat_map { |v, i| dependency.call(v, "#{path}[#{i}]") }
    when Hash
      assert(value['use'].is_a?(String), "invalid inline plugin #{path}")
      visit.call(path, value)
      [path]
    else raise "invalid dependency #{path}"
    end
  end
  visit = lambda do |id, node|
    assert(!nodes.key?(id), "duplicate instance #{id}")
    assert(node.is_a?(Hash) && node['use'].is_a?(String), "invalid instance #{id}")
    nodes[id] = node
    deps = node.fetch('deps', {})
    assert(deps.is_a?(Hash), "invalid deps #{id}")
    edges[id] = deps.flat_map { |name, value| dependency.call(value, "#{id}.deps.#{name}") }
  end
  graph.each { |id, node| visit.call(id, node) }
  [nodes, edges]
end

def check_graph(graph, label, quiet: false)
  nodes, edges = flatten_graph(graph)
  seen = {}
  active = []
  walk = lambda do |id|
    assert(nodes.key?(id), "#{label}: missing instance #{id}")
    assert(!active.include?(id), "#{label}: cycle #{(active + [id]).join(' -> ')}")
    return if seen[id]
    active << id
    edges.fetch(id).each { |ref| walk.call(ref) }
    active.pop
    seen[id] = true
  end
  walk.call('server')
  assert(seen.size == nodes.size, "#{label}: unreachable instances #{nodes.keys - seen.keys}")
  assert(graph.fetch('server')['use'] == 'server', "#{label}: invalid root")
  assert(graph['server'].fetch('deps').fetch('components') == ['http'], "#{label}: current HTTP owner missing")
  resources = graph['server']['deps'].fetch('resources')
  expected = %w[api/health server transport/http]
  if nodes.key?('sql')
    assert(resources == ['sql'], "#{label}: SQL resource owner missing")
    assert(nodes['sql']['use'] == 'storage/sql', "#{label}: invalid storage kind")
    expected << 'storage/sql'
  else
    assert(resources == [], "#{label}: unexpected resource")
  end
  assert(nodes.values.map { |n| n['use'] }.sort == expected.sort, "#{label}: default graph contains unexpected capabilities")
  assert(graph.fetch('http').fetch('deps').fetch('modules') == ['health'], "#{label}: health module owner missing")
  puts "PASS #{label}: references, acyclic/reachable graph and current owners" unless quiet
end

base = YAML.load_file('base.yaml')
override = YAML.load_file('config.yaml')
valid = merge_instances(base, override)
check_graph(valid, 'root base + config')
check_graph(merge_instances(YAML.load_file('examples/minimal/base.yaml'), YAML.load_file('examples/minimal/config.yaml')), 'minimal example')
Dir['examples/flows/*.config.yaml'].each do |path|
  snippet = YAML.load_file(path)
  assert(snippet == {}, "retired flow snippet must not enable capabilities: #{path}")
end
[
  ['missing reference', lambda { |g| g['http']['deps']['modules'] = ['missing'] }],
  ['dependency cycle', lambda { |g| g['http']['deps']['modules'] = ['server'] }],
  ['missing owner', lambda { |g| g['server']['deps']['components'] = [] }],
  ['unreachable node', lambda { |g| g['orphan'] = {'use'=>'api/health'} }]
].each do |label, mutate|
  graph = Marshal.load(Marshal.dump(valid))
  mutate.call(graph)
  rejected = false
  begin
    check_graph(graph, label, quiet: true)
  rescue RuntimeError
    rejected = true
  end
  assert(rejected, "negative graph accepted: #{label}")
end
puts 'PASS negative reference/cycle/owner/reachability checks'

links = 0
files = %w[README.md AGENTS.md todo.md] + Dir['docs/*.md'] + Dir['examples/*.md']
files.each do |path|
  text = File.read(path)
  assert(text.scan(/^```/).size.even?, "unclosed code fence #{path}")
  text.scan(/\]\(([^)]+)\)/).flatten.each do |target|
    next if target.include?('://') || target.start_with?('#')
    local = target.split('#').first.sub(/:\d+\z/, '')
    # Nex source links are workstation-specific research pointers, not repo deps.
    next if Pathname.new(local).absolute?
    assert(File.exist?(File.join(File.dirname(path), local)), "broken link #{path}: #{target}")
    links += 1
  end
end
cases = File.read('docs/core-contracts.md').scan(/^\| (C\d{2}) \|/).flatten
assert(cases.sort == (1..18).map { |n| format('C%02d', n) }, 'core contract case IDs missing or duplicated')
todo = File.read('todo.md')
# Development task prerequisites are a separate graph from plugin dependencies.
task_deps = {}
todo.each_line do |line|
  match = line.match(/^- \[.\] (M\d{2}) /)
  next unless match
  id = match[1]
  clause = line[/依赖 ([^。]+)。/, 1].to_s
  expanded = clause.gsub(/M(\d{2})[—–-]M(\d{2})/) do
    first, last = Regexp.last_match(1).to_i, Regexp.last_match(2).to_i
    assert(first <= last, "invalid TODO range #{id}")
    (first..last).map { |n| format('M%02d', n) }.join('、')
  end
  assert(!task_deps.key?(id), "duplicate task #{id}")
  task_deps[id] = expanded.scan(/M\d{2}/).uniq
end
assert(task_deps.keys.sort == (1..11).map { |n| format('M%02d', n) }, 'missing development tasks')
checked_tasks = {}
visit_task = lambda do |id, path|
  assert(task_deps.key?(id), "missing prerequisite #{id}")
  assert(!path.include?(id), "TODO dependency cycle #{(path + [id]).join(' -> ')}")
  next if checked_tasks[id]
  task_deps[id].each { |dep| visit_task.call(dep, path + [id]) }
  checked_tasks[id] = true
end
task_deps.each_key { |id| visit_task.call(id, []) }
puts 'PASS M01-M11 prerequisite references and acyclic development order'
puts "PASS document links (#{links}), fences, C01-C18 coverage and scoped TODO status"
puts 'STATIC CHECK ONLY: real Go/PluginKit validation is provided separately by scripts/check.sh; business behavior and external integrations are not verified here.'
