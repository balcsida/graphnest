package example.service;

import example.base.Parent;
import example.api.*;

interface Runnable {
    void run();
}

class Service extends Parent implements Runnable {
    void run() {
        this.run();
        worker.work();
    }
}
