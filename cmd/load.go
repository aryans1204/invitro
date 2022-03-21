package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"

	tracer "github.com/ease-lab/vhive/utils/tracing/go"
	wu "github.com/eth-easl/loader/cmd/options"
	fc "github.com/eth-easl/loader/pkg/function"
	gen "github.com/eth-easl/loader/pkg/generate"
	tc "github.com/eth-easl/loader/pkg/trace"
)

const (
	zipkinAddr = "http://localhost:9411/api/v2/spans"
)

var (
	traces            tc.FunctionTraces
	serviceConfigPath = "workloads/trace_func_go.yaml"

	mode        = flag.String("mode", "trace", "Choose a mode from [trace, stress]")
	debug       = flag.Bool("dbg", false, "Enable debug logging")
	cluster     = flag.Int("cluster", 1, "Size of the cluster measured by #workers")
	duration    = flag.Int("duration", 3, "Duration of the experiment")
	sampleSize  = flag.Int("sample", 10, "Sample size of the traces")
	withTracing = flag.Bool("trace", false, "Enable tracing in the client")
	rps         = flag.Int("rps", -900_000, "Request per second")
	rpsStart    = flag.Int("start", 0, "Starting RPS value")
	rpsSlot     = flag.Int("slot", 1, "Time slot in minutes for each RPS in the `stress` mode")
	rpsStep     = flag.Int("step", 1, "Step size for increasing RPS in the `stress` mode")

	seed = flag.Int64("seed", 42, "Random seed for the generator")

	// withWarmup = flag.Int("withWarmup", -1000, "Duration of the withWarmup")
	withWarmup = flag.Bool("warmup", false, "Enable warmup")
)

func init() {
	/** Logging. */
	flag.Parse()

	log.SetFormatter(&log.TextFormatter{
		TimestampFormat: time.StampMilli,
		FullTimestamp:   true,
	})
	log.SetOutput(os.Stdout)
	if *debug {
		log.SetLevel(log.DebugLevel)
		log.Debug("Debug logging is enabled")
	} else {
		log.SetLevel(log.InfoLevel)
	}
	if *withTracing {
		shutdown, err := tracer.InitBasicTracer(zipkinAddr, "loader")
		if err != nil {
			log.Print(err)
		}
		defer shutdown()
	}
}

func main() {
	gen.InitSeed(*seed)

	switch *mode {
	case "trace":
		runTraceMode()
	case "stress":
		runStressMode()
	default:
		log.Fatal("Invalid mode: ", *mode)
	}
}

func runTraceMode() {
	/** Trace parsing */
	traces = tc.ParseInvocationTrace(
		"data/traces/"+strconv.Itoa(*sampleSize)+"_inv.csv", *duration)
	tc.ParseDurationTrace(
		&traces, "data/traces/"+strconv.Itoa(*sampleSize)+"_run.csv")
	tc.ParseMemoryTrace(
		&traces, "data/traces/"+strconv.Itoa(*sampleSize)+"_mem.csv")

	log.Info("Traces contain the following: ", len(traces.Functions), " functions")
	for _, function := range traces.Functions {
		fmt.Println("\t" + function.GetName())
	}

	totalNumPhases := 3
	profilingMinutes := *duration/2 + 1 //TODO

	/* Profiling */
	if *withWarmup {
		for funcIdx := 0; funcIdx < len(traces.Functions); funcIdx++ {
			function := traces.Functions[funcIdx]
			traces.Functions[funcIdx].ConcurrencySats =
				tc.ProfileFunctionConcurrencies(function, profilingMinutes)
		}
		traces.WarmupScales = wu.ComputeFunctionsWarmupScales(*cluster, traces.Functions)
	}

	/** Deployment */
	functions := fc.DeployTrace(traces.Functions, serviceConfigPath, traces.WarmupScales)

	/** Warmup (Phase 1 and 2) */
	nextPhaseStart := 0
	if *withWarmup {
		nextPhaseStart = wu.Warmup(*sampleSize, totalNumPhases, *rps, functions, traces)
	}

	/** Measurement (Phase 3) */
	if nextPhaseStart == *duration {
		gen.DumpOverloadFlag()
		log.Infof("Warmup failed to finish in %d minutes", *duration)
	}
	log.Infof("Phase 3: Generate real workloads as of Minute[%d]", nextPhaseStart)
	defer gen.GenerateTraceLoads(
		*sampleSize,
		totalNumPhases,
		nextPhaseStart,
		true,
		*rps,
		functions,
		traces.InvocationsEachMinute[nextPhaseStart:],
		traces.TotalInvocationsPerMinute[nextPhaseStart:])
}

func runStressMode() {
	function := tc.Function{
		Name:     "stress-func",
		Endpoint: tc.GetFuncEndpoint("stress-func"),
	}
	fc.DeployFunction(&function, serviceConfigPath, 0)

	defer gen.GenerateStressLoads(*rpsStart, *rpsStep, *rpsSlot, function)
}
