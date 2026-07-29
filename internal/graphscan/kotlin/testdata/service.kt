package example.service

import example.base.Parent as Base
import example.api.*

interface Worker {
    fun run()
}

fun helper() {}

class Service : Base(), Worker {
    override fun run() {
        this.run()
        helper()
    }
}

object Singleton
