def IMAGE_TAG = ""
pipeline {
    agent {
        kubernetes {
            yaml """
apiVersion: v1
kind: Pod
metadata:
  name: kaniko
spec:
  containers:
  - name: kaniko
    image: gcr.io/kaniko-project/executor:debug
    imagePullPolicy: Always
    command:
    - /busybox/cat
    tty: true
    volumeMounts:
      - name: jenkins-docker-cfg
        mountPath: /kaniko/.docker
  volumes:
  - name: jenkins-docker-cfg
    projected:
      sources:
      - secret:
          name: docker-credentials
          items:
            - key: data
              path: config.json
"""
        }
    }
    environment {
        IMAGE_PUSH_DESTINATION="ghcr.io/ruakij/routingtabletowg"
    }
    stages {
        stage("Pre-build") {
            steps {

                script{
                    //checkout scm
                    checkout([
                        $class: 'GitSCM',
                        branches: scm.branches,
                        doGenerateSubmoduleConfigurations: scm.doGenerateSubmoduleConfigurations,
                        extensions: scm.extensions + [[$class: 'CloneOption', noTags: false, reference: '', shallow: true]],
                        submoduleCfg: [],
                        userRemoteConfigs: scm.userRemoteConfigs
                    ])

                    def version = sh (returnStdout: true, script: "git describe --tags --long --always $GIT_COMMIT").trim()
                    def gitCommit = sh (returnStdout: true, script: "git rev-parse --short $GIT_COMMIT").trim()
                    echo "Version: $version"
                    echo "Git Commit: $gitCommit"

                    IMAGE_TAG = "--destination $IMAGE_PUSH_DESTINATION:$gitCommit "
                    
                    if (GIT_BRANCH == "main") {
                        IMAGE_TAG += "--destination $IMAGE_PUSH_DESTINATION:latest "

                        if(version != gitCommit){
                            def parts = version.split('.')
                            if(parts.size() > 0){
                                for (int i = 0; i < parts.size(); i++) {
                                    def versionTag = parts[0..i].join(".")
                                    IMAGE_TAG += "--destination $IMAGE_PUSH_DESTINATION:$versionTag "
                                }
                            }
                        }
                    } else {
                        IMAGE_TAG += "--destination $IMAGE_PUSH_DESTINATION:$GIT_BRANCH "

                        if(version != gitCommit){
                            def parts = version.split('.')
                            if(parts.size() > 0){
                                for (int i = 0; i < parts.size(); i++) {
                                    def versionTag = parts[0..i].join(".")
                                    IMAGE_TAG += "--destination $IMAGE_PUSH_DESTINATION:$GIT_BRANCH-$versionTag "
                                }
                            }
                        }
                    }
                    
                    echo "Image-Tags: $IMAGE_TAG"
                }
            }
        }
        
        stage('Build with Kaniko') {
            steps {
                container(name: 'kaniko', shell: '/busybox/sh') {
                    withEnv(['PATH+EXTRA=/busybox', "IMAGE_TAG=$IMAGE_TAG"]) {
                        // Use the image tag variable as part of the image name when you build and push the image with kaniko
                        sh '''#!/busybox/sh
                            /kaniko/executor --context `pwd` --force $IMAGE_TAG
                        '''
                    }
                }
            }
        }
    }
}
