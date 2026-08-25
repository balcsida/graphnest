require "fileutils"
require "optparse"
require "yaml"

VERSION = /\A\d+\.\d+\.\d+\z/
DIGEST = /\Asha256:[0-9a-f]{64}\z/
REPOSITORY = /\A(?!.*@)(?!.*:latest\z)[a-z0-9]+(?:[._-][a-z0-9]+)*(?:\/[a-z0-9]+(?:[._-][a-z0-9]+)*)+\z/

options = {}
parser = OptionParser.new do |arguments|
  arguments.on("--chart PATH") { |value| options[:chart] = value }
  arguments.on("--output PATH") { |value| options[:output] = value }
  arguments.on("--version X.Y.Z") { |value| options[:version] = value }
  arguments.on("--application-repository REPOSITORY") { |value| options[:application_repository] = value }
  arguments.on("--application-digest sha256:HEX") { |value| options[:application_digest] = value }
  arguments.on("--node-repository REPOSITORY") { |value| options[:node_repository] = value }
  arguments.on("--node-digest sha256:HEX") { |value| options[:node_digest] = value }
end

begin
  parser.parse!
  required = %i[chart output version application_repository application_digest node_repository node_digest]
  missing = required.reject { |key| options.key?(key) }
  raise "missing required options: #{missing.join(", ")}" unless missing.empty?
  raise "invalid version" unless VERSION.match?(options[:version])
  raise "invalid application repository" unless REPOSITORY.match?(options[:application_repository])
  raise "invalid node repository" unless REPOSITORY.match?(options[:node_repository])
  raise "invalid application digest" unless DIGEST.match?(options[:application_digest])
  raise "invalid node digest" unless DIGEST.match?(options[:node_digest])

  source = File.realpath(options[:chart])
  output = File.expand_path(options[:output])
  nested = source == output || output.start_with?("#{source}/") || source.start_with?("#{output}/")
  raise "source and output paths must not overlap" if nested
  raise "output path is not a directory" if File.exist?(output) && !File.directory?(output)
  raise "output directory must be empty" if File.directory?(output) && !Dir.empty?(output)

  FileUtils.mkdir_p(output)
  staged = File.join(output, "graphnest")
  FileUtils.cp_r(source, staged)

  chart_path = File.join(staged, "Chart.yaml")
  values_path = File.join(staged, "values.yaml")
  chart = YAML.safe_load(File.read(chart_path), aliases: false)
  values = YAML.safe_load(File.read(values_path), aliases: false)
  chart["version"] = options.fetch(:version)
  chart["appVersion"] = options.fetch(:version)
  values["images"]["application"].merge!(
    "repository" => options.fetch(:application_repository),
    "digest" => options.fetch(:application_digest),
    "tag" => "",
  )
  values["images"]["node"].merge!(
    "repository" => options.fetch(:node_repository),
    "digest" => options.fetch(:node_digest),
    "tag" => "",
  )
  File.write(chart_path, YAML.dump(chart))
  File.write(values_path, YAML.dump(values))
rescue OptionParser::ParseError, Errno::ENOENT, RuntimeError => error
  warn "error: #{error.message}"
  exit 1
end
