mod service {
    use crate::jobs::{Job, Queue as WorkQueue};

    trait Run {
        fn run(&self);
    }

    struct Job;

    impl Job {
        fn new() -> Self { Job }
    }

    impl Run for Job {
        fn run(&self) {}
    }

    fn start() {}

    fn drive(job: Job) {
        start();
        job.run();
    }
}
