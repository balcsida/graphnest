require "open3"
require "tmpdir"
require "yaml"

def assert(condition, message)
  raise message unless condition
end

root = File.expand_path("..", __dir__)
script = File.join(root, "scripts/stage_release_chart.rb")
chart = File.join(root, "deploy/helm/grepnest")
version = "0.1.0"
application_repository = "ghcr.io/balcsida/grep-nest/application"
node_repository = "ghcr.io/balcsida/grep-nest/node"
application_digest = "sha256:" + "a" * 64
node_digest = "sha256:" + "b" * 64

def stage(script, chart, output, version, application_repository, application_digest,
          node_repository, node_digest)
  Open3.capture3(
    "ruby", script,
    "--chart", chart,
    "--output", output,
    "--version", version,
    "--application-repository", application_repository,
    "--application-digest", application_digest,
    "--node-repository", node_repository,
    "--node-digest", node_digest,
  )
end

Dir.mktmpdir do |directory|
  output = File.join(directory, "release")
  stdout, stderr, status = stage(
    script, chart, output, version, application_repository, application_digest,
    node_repository, node_digest,
  )
  assert(status.success?, "staging failed: #{stdout}#{stderr}")

  staged = File.join(output, "grepnest")
  staged_chart = YAML.load_file(File.join(staged, "Chart.yaml"))
  staged_values = YAML.load_file(File.join(staged, "values.yaml"))
  assert(staged_chart["version"] == version, "staged chart version is wrong")
  assert(staged_chart["appVersion"] == version, "staged app version is wrong")
  assert(staged_values.dig("images", "application", "repository") == application_repository,
         "staged application repository is wrong")
  assert(staged_values.dig("images", "application", "digest") == application_digest,
         "staged application digest is wrong")
  assert(staged_values.dig("images", "application", "tag") == "", "staged application tag is wrong")
  assert(staged_values.dig("images", "node", "repository") == node_repository,
         "staged node repository is wrong")
  assert(staged_values.dig("images", "node", "digest") == node_digest,
         "staged node digest is wrong")
  assert(staged_values.dig("images", "node", "tag") == "", "staged node tag is wrong")

  [
    ["v0.1.0", application_repository, application_digest, node_repository, node_digest],
    ["latest", application_repository, application_digest, node_repository, node_digest],
    [version, "#{application_repository}:tag", application_digest, node_repository, node_digest],
    [version, application_repository, "sha256:" + "a" * 63, node_repository, node_digest],
  ].each_with_index do |arguments, index|
    _, _, invalid_status = stage(script, chart, File.join(directory, "invalid-#{index}"), *arguments)
    assert(!invalid_status.success?, "invalid arguments #{index} succeeded")
  end

  _, _, equal_status = stage(script, chart, chart, version, application_repository, application_digest,
                              node_repository, node_digest)
  assert(!equal_status.success?, "equal source and output paths succeeded")

  nonempty_output = File.join(directory, "nonempty")
  Dir.mkdir(nonempty_output)
  File.write(File.join(nonempty_output, "existing"), "x")
  _, _, nonempty_status = stage(script, chart, nonempty_output, version, application_repository,
                                 application_digest, node_repository, node_digest)
  assert(!nonempty_status.success?, "nonempty output directory succeeded")
end

puts "stage release chart tests passed"
